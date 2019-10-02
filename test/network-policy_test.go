package test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/cybozu-go/log"
	"github.com/cybozu-go/sabakan/v2"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func testNetworkPolicy() {
	It("should create test-netpol namespace", func() {
		ExecSafeAt(boot0, "kubectl", "delete", "namespace", "test-netpol", "--ignore-not-found=true")
		ExecSafeAt(boot0, "kubectl", "create", "namespace", "test-netpol")
	})

	It("should create test pods", func() {
		By("deploying testhttpd pods")
		deployYAML := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: testhttpd
  namespace: test-netpol
spec:
  replicas: 2
  selector:
    matchLabels:
      app.kubernetes.io/name: testhttpd
  template:
    metadata:
      labels:
        app.kubernetes.io/name: testhttpd
    spec:
      containers:
      - image: quay.io/cybozu/testhttpd:0
        name: testhttpd
      restartPolicy: Always
---
apiVersion: v1
kind: Service
metadata:
  name: testhttpd
  namespace: test-netpol
spec:
  ports:
  - port: 80
    protocol: TCP
    targetPort: 8000
  selector:
    app.kubernetes.io/name: testhttpd
`
		_, stderr, err := ExecAtWithInput(boot0, []byte(deployYAML), "kubectl", "apply", "-f", "-")
		Expect(err).NotTo(HaveOccurred(), "stderr: %s", stderr)

		By("waiting pods are ready")
		Eventually(func() error {
			stdout, _, err := ExecAt(boot0, "kubectl", "-n", "test-netpol", "get", "deployments/testhttpd", "-o", "json")
			if err != nil {
				return err
			}

			deployment := new(appsv1.Deployment)
			err = json.Unmarshal(stdout, deployment)
			if err != nil {
				return err
			}

			if deployment.Status.ReadyReplicas != 2 {
				return errors.New("ReadyReplicas is not 2")
			}
			return nil
		}).Should(Succeed())

		// connections to 8080 and 8443 of contour are rejected unless we register IngressRoute
		By("creating IngressRoute")
		fqdnHTTP := testID + "-http.test-netpol.gcp0.dev-ne.co"
		fqdnHTTPS := testID + "-https.test-netpol.gcp0.dev-ne.co"
		ingressRoute := fmt.Sprintf(`
apiVersion: contour.heptio.com/v1beta1
kind: IngressRoute
metadata:
  name: tls
  namespace: test-netpol
  annotations:
    kubernetes.io/tls-acme: "true"
spec:
  virtualhost:
    fqdn: %s
    tls:
      secretName: testsecret
  routes:
    - match: /
      services:
        - name: testhttpd
          port: 80
    - match: /insecure
      permitInsecure: true
      services:
        - name: testhttpd
          port: 80
---
apiVersion: contour.heptio.com/v1beta1
kind: IngressRoute
metadata:
  name: root
  namespace: test-netpol
spec:
  virtualhost:
    fqdn: %s
  routes:
    - match: /testhttpd
      services:
        - name: testhttpd
          port: 80
`, fqdnHTTPS, fqdnHTTP)
		_, stderr, err = ExecAtWithInput(boot0, []byte(ingressRoute), "kubectl", "apply", "-f", "-")
		Expect(err).NotTo(HaveOccurred(), "stderr: %s", stderr)

		By("deploying ubuntu for network commands")
		createUbuntuDebugPod("default")
	})

	testhttpdPodList := new(corev1.PodList)
	nodeList := new(corev1.NodeList)
	var nodeIP string
	var apiServerIP string

	It("should get pod/node list", func() {

		By("getting httpd pod list")
		stdout, stderr, err := ExecAt(boot0, "kubectl", "get", "pods", "-n", "test-netpol", "-o=json")
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)
		err = json.Unmarshal(stdout, testhttpdPodList)
		Expect(err).NotTo(HaveOccurred())

		By("getting all node list")
		stdout, stderr, err = ExecAt(boot0, "kubectl", "get", "node", "-o=json")
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)
		err = json.Unmarshal(stdout, nodeList)
		Expect(err).NotTo(HaveOccurred())

		By("getting a certain node IP address")
	OUTER:
		for _, node := range nodeList.Items {
			for _, addr := range node.Status.Addresses {
				if addr.Type == "InternalIP" {
					nodeIP = addr.Address
					break OUTER
				}
			}
		}
		Expect(nodeIP).NotTo(BeEmpty())

		stdout, stderr, err = ExecAt(boot0, "kubectl", "config", "view", "--output=jsonpath={.clusters[0].cluster.server}")
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)
		u, err := url.Parse(string(stdout))
		Expect(err).NotTo(HaveOccurred(), "server: %s", stdout)
		apiServerIP = strings.Split(u.Host, ":")[0]
		Expect(apiServerIP).NotTo(BeEmpty(), "server: %s", stdout)
	})

	It("should resolve hostname with DNS", func() {
		By("resolving hostname inside of cluster (by cluster-dns)")
		Eventually(func() error {
			stdout, stderr, err := ExecAt(boot0, "kubectl", "exec", "ubuntu", "--", "nslookup", "-timeout=10", "testhttpd.test-netpol")
			if err != nil {
				return fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}
			return nil
		}).Should(Succeed())

		By("resolving hostname outside of cluster (by unbound)")
		Eventually(func() error {
			stdout, stderr, err := ExecAt(boot0, "kubectl", "exec", "ubuntu", "--", "nslookup", "-timeout=10", "cybozu.com")
			if err != nil {
				return fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}
			return nil
		}).Should(Succeed())
	})

	It("should filter packets from squid/unbound to private network", func() {
		By("accessing to local IP")
		stdout, stderr, err := ExecAt(boot0, "kubectl", "-n", "internet-egress", "get", "pods", "-o=json")
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)
		podList := new(corev1.PodList)
		err = json.Unmarshal(stdout, podList)
		Expect(err).NotTo(HaveOccurred())
		testhttpdIP := testhttpdPodList.Items[0].Status.PodIP

		for _, pod := range podList.Items {
			stdout, stderr, err := ExecAt(boot0, "kubectl", "exec", "-n", pod.Namespace, pod.Name, "--", "curl", testhttpdIP, "-m", "5")
			Expect(err).To(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)
		}

		if withKind {
			Skip("does not make sense with kindtest")
		}

		By("patching squid pods to add ubuntu-debug sidecar container")
		stdout, stderr, err = ExecAt(boot0,
			"kubectl", "patch", "-n=internet-egress", "deploy", "squid", "--type=json",
			`-p='[{"op": "add", "path": "/spec/template/spec/containers/-", "value": { "image": "quay.io/cybozu/ubuntu-debug:18.04", "imagePullPolicy": "IfNotPresent", "name": "ubuntu", "command": ["pause"] }}]'`,
		)
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)

		By("waiting deployment is ready")
		Eventually(func() error {
			stdout, stderr, err := ExecAt(boot0, "kubectl", "get", "deployment", "-n=internet-egress", "squid", "-o", "json")
			if err != nil {
				return fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}

			var deploy appsv1.Deployment
			err = json.Unmarshal(stdout, &deploy)
			if err != nil {
				return fmt.Errorf("err: %v, stdout: %s", err, stdout)
			}

			if deploy.Status.ReadyReplicas != 2 {
				return fmt.Errorf("the number of replicas should be 2: %d", deploy.Status.ReadyReplicas)
			}
			return nil
		}).Should(Succeed())

		By("accessing DNS port of some node as squid")
		stdout, stderr, err = ExecAt(boot0, "kubectl", "get", "pods", "-n=internet-egress", "-l=app.kubernetes.io/name=squid", "-o", "go-template='{{ (index .items 0).metadata.name }}'")
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)
		podName := string(stdout)

		Eventually(func() error {
			stdout, stderr, err = ExecAtWithInput(boot0, []byte("Xclose"), "kubectl", "-n", "internet-egress", "exec", "-i", podName, "-c", "ubuntu", "--", "timeout", "3s", "telnet", nodeIP, "53", "-e", "X")
			switch t := err.(type) {
			case *ssh.ExitError:
				// telnet command returns 124 when it times out
				if t.ExitStatus() != 124 {
					return fmt.Errorf("exit status should be 124: %d", t.ExitStatus())
				}
			case *exec.ExitError:
				if t.ExitCode() != 124 {
					return fmt.Errorf("exit status should be 124: %d", t.ExitCode())
				}
			default:
				return errors.New("telnet should fail with timeout")
			}
			return nil
		}).Should(Succeed())

		By("patching unbound pods to add ubuntu-debug sidecar container")
		stdout, stderr, err = ExecAt(boot0,
			"kubectl", "patch", "-n=internet-egress", "deploy", "unbound", "--type=json",
			`-p='[{"op": "add", "path": "/spec/template/spec/containers/-", "value": { "image": "quay.io/cybozu/ubuntu-debug:18.04", "imagePullPolicy": "IfNotPresent", "name": "ubuntu", "command": ["pause"] }}]'`,
		)
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)

		By("waiting deployment is ready")
		Eventually(func() error {
			stdout, stderr, err := ExecAt(boot0, "kubectl", "get", "deployment", "-n=internet-egress", "unbound", "-o", "json")
			if err != nil {
				return fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}

			var deploy appsv1.Deployment
			err = json.Unmarshal(stdout, &deploy)
			if err != nil {
				return fmt.Errorf("err: %v, stdout: %s", err, stdout)
			}

			if deploy.Status.ReadyReplicas != 2 {
				return fmt.Errorf("the number of replicas should be 2: %d", deploy.Status.ReadyReplicas)
			}
			return nil
		}).Should(Succeed())

		By("accessing DNS port of some node as unbound")
		stdout, stderr, err = ExecAt(boot0, "kubectl", "get", "pods", "-n=internet-egress", "-l=app.kubernetes.io/name=unbound", "-o", "go-template='{{ (index .items 0).metadata.name }}'")
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)
		podName = string(stdout)

		Eventually(func() error {
			stdout, stderr, err = ExecAtWithInput(boot0, []byte("Xclose"), "kubectl", "-n", "internet-egress", "exec", "-i", podName, "-c", "ubuntu", "--", "timeout", "3s", "telnet", nodeIP, "53", "-e", "X")
			switch t := err.(type) {
			case *ssh.ExitError:
				// telnet command returns 124 when it times out
				if t.ExitStatus() != 124 {
					return fmt.Errorf("exit status should be 124: %d", t.ExitStatus())
				}
			case *exec.ExitError:
				if t.ExitCode() != 124 {
					return fmt.Errorf("exit status should be 124: %d", t.ExitCode())
				}
			default:
				return errors.New("telnet should fail with timeout")
			}
			return nil
		}).Should(Succeed())
	})

	It("should pass packets to node network for system services", func() {
		if withKind {
			Skip("does not make sense with kindtest")
		}

		By("accessing DNS port of some node")
		stdout, stderr, err := ExecAtWithInput(boot0, []byte("Xclose"), "kubectl", "exec", "-i", "ubuntu", "--", "timeout", "3s", "telnet", nodeIP, "53", "-e", "X")
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)

		By("accessing API server port of control plane node")
		stdout, stderr, err = ExecAtWithInput(boot0, []byte("Xclose"), "kubectl", "exec", "-i", "ubuntu", "--", "timeout", "3s", "telnet", apiServerIP, "6443", "-e", "X")
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)

		By("patching prometheus pods to add ubuntu-debug sidecar container")
		stdout, stderr, err = ExecAt(boot0,
			"kubectl", "patch", "-n=monitoring", "statefulset", "prometheus", "--type=json",
			`-p='[{"op": "add", "path": "/spec/template/spec/containers/-", "value": { "image": "quay.io/cybozu/ubuntu-debug:18.04", "imagePullPolicy": "IfNotPresent", "name": "ubuntu", "command": ["pause"] }}]'`,
		)
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)

		By("waiting statefulset is ready")
		Eventually(func() error {
			stdout, stderr, err := ExecAt(boot0, "kubectl", "get", "statefulset", "-n=monitoring", "prometheus", "-o", "json")
			if err != nil {
				return fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}

			var ss appsv1.StatefulSet
			err = json.Unmarshal(stdout, &ss)
			if err != nil {
				return fmt.Errorf("err: %v, stdout: %s", err, stdout)
			}

			if ss.Status.ReadyReplicas != 1 {
				return fmt.Errorf("the number of replicas should be 1: %d", ss.Status.ReadyReplicas)
			}
			return nil
		}).Should(Succeed())

		By("accessing DNS port of some node as prometheus")
		stdout, stderr, err = ExecAt(boot0, "kubectl", "get", "pods", "-n=monitoring", "-l=app.kubernetes.io/name=prometheus", "-o", "go-template='{{ (index .items 0).metadata.name }}'")
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)
		podName := string(stdout)

		Eventually(func() error {
			stdout, stderr, err = ExecAtWithInput(boot0, []byte("Xclose"), "kubectl", "-n", "monitoring", "exec", "-i", podName, "-c", "ubuntu", "--", "timeout", "3s", "telnet", nodeIP, "9100", "-e", "X")
			switch t := err.(type) {
			case *ssh.ExitError:
				// telnet command returns 124 when it times out
				if t.ExitStatus() != 124 {
					return fmt.Errorf("exit status should be 124: %d", t.ExitStatus())
				}
			case *exec.ExitError:
				if t.ExitCode() != 124 {
					return fmt.Errorf("exit status should be 124: %d", t.ExitCode())
				}
			default:
				return errors.New("telnet should fail with timeout")
			}
			return nil
		}).Should(Succeed())
	})

	It("should filter icmp packets to BMC/Node/Bastion/switch networks", func() {
		if withKind {
			Skip("does not make sense with kindtest")
		}

		stdout, stderr, err := ExecAt(boot0, "sabactl", "machines", "get")
		Expect(err).NotTo(HaveOccurred(), "stdout: %s, stderr: %s", stdout, stderr)

		var machines []sabakan.Machine
		err = json.Unmarshal(stdout, &machines)
		Expect(err).ShouldNot(HaveOccurred())

		eg := errgroup.Group{}
		ping := func(addr string) error {
			_, _, err := ExecAt(boot0, "kubectl", "exec", "ubuntu", "--", "ping", "-c", "1", "-W", "3", addr)
			if err != nil {
				return err
			}
			log.Error("ping should be failed, but it was succeeded", map[string]interface{}{
				"target": addr,
			})
			return nil
		}
		for _, m := range machines {
			bmcAddr := m.Spec.BMC.IPv4
			node0Addr := m.Spec.IPv4[0]
			eg.Go(func() error {
				return ping(bmcAddr)
			})
			eg.Go(func() error {
				return ping(node0Addr)
			})
		}
		// Bastion
		eg.Go(func() error {
			return ping(boot0)
		})
		Expect(eg.Wait()).Should(HaveOccurred())
		// switch -- not tested for now because address range for switches is 10.0.1.0/24 in placemat env, not 10.72.0.0/20.
	})

	It("should deny network policy in non-system namespace with order <= 1000", func() {
		By("creating invalid network policy")
		policyYAML := `
apiVersion: crd.projectcalico.org/v1
kind: NetworkPolicy
metadata:
  name: ingress-httpdtest-high-prio
  namespace: test-netpol
spec:
  order: 1000.0
  selector: app.kubernetes.io/name == 'testhttpd'
  types:
    - Ingress
  ingress:
    - action: Allow
      protocol: TCP
      destination:
        ports:
          - 8000
`
		_, stderr, err := ExecAtWithInput(boot0, []byte(policyYAML), "kubectl", "apply", "-f", "-")
		Expect(err).To(HaveOccurred())
		Expect(string(stderr)).To(ContainSubstring("cannot create/update non-system NetworkPolicy with order <= 1000"))
	})
}

func createUbuntuDebugPod(namespace string) {
	debugYAML := `
apiVersion: v1
kind: Pod
metadata:
  name: ubuntu
spec:
  securityContext:
    runAsUser: 10000
    runAsGroup: 10000
  containers:
  - name: ubuntu
    image: quay.io/cybozu/ubuntu-debug:18.04
    command: ["sleep", "infinity"]`
	_, stderr, err := ExecAtWithInput(boot0, []byte(debugYAML), "kubectl", "apply", "-n", namespace, "-f", "-")
	Expect(err).NotTo(HaveOccurred(), "stderr: %s", stderr)

	By("waiting for ubuntu pod to start")
	Eventually(func() error {
		stdout, stderr, err := ExecAt(boot0, "kubectl", "-n", namespace, "exec", "ubuntu", "--", "date")
		if err != nil {
			return fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
		}
		return nil
	}).Should(Succeed())
}
