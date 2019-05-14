package ingress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cybozu-go/neco-ops/test"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func testExternalDNS() {
	It("should be deployed successfully", func() {
		Eventually(func() error {
			stdout, _, err := test.ExecAt(test.Boot0, "kubectl", "--namespace=ingress",
				"get", "deployment/external-dns", "-o=json")
			if err != nil {
				return err
			}

			deployment := new(appsv1.Deployment)
			err = json.Unmarshal(stdout, deployment)
			if err != nil {
				return err
			}

			if deployment.Status.AvailableReplicas != 1 {
				return fmt.Errorf("external-dns deployment's AvailableReplica is not 1: %d", int(deployment.Status.AvailableReplicas))
			}
			return nil
		}).Should(Succeed())
	})

	It("should create DNS record", func() {
		By("deploying DNSEndpoint")
		dnsEndpoint := fmt.Sprintf(`
apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
metadata:
  name: dnsendpoint
  namespace: test-ingress
spec:
  endpoints:
  - dnsName: %s.neco-ops.cybozu-ne.co
    recordTTL: 180
    recordType: A
    targets:
    - 10.0.5.9
`, test.TestID)
		_, stderr, err := test.ExecAtWithInput(test.Boot0, []byte(dnsEndpoint), "kubectl", "apply", "-f", "-")
		Expect(err).NotTo(HaveOccurred(), "stderr: %s", stderr)

		By("resolving xxx.neco-ops.cybozu-ne.co")
		ubuntu := `
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  labels:
    run: test-ubuntu
  name: test-ubuntu
  namespace: internet-egress
spec:
  selector:
    matchLabels:
      run: test-ubuntu
  template:
    metadata:
      labels:
        run: test-ubuntu
    spec:
      containers:
      - name: test-ubuntu
        command: ["/bin/sleep", "Infinity"]
        image: quay.io/cybozu/ubuntu-debug:18.04
        imagePullPolicy: IfNotPresent
      securityContext:
        runAsNonRoot: true
        runAsUser: 65534 # nobody
`
		_, stderr, err = test.ExecAtWithInput(test.Boot0, []byte(ubuntu), "kubectl", "apply", "-f", "-")
		Expect(err).NotTo(HaveOccurred(), "stderr: %s", stderr)

		Eventually(func() error {
			stdout, _, err := test.ExecAt(test.Boot0, "kubectl", "-n", "internet-egress", "get", "deployments/test-ubuntu", "-o", "json")
			if err != nil {
				return err
			}

			deployment := new(appsv1.Deployment)
			err = json.Unmarshal(stdout, deployment)
			if err != nil {
				return err
			}

			if deployment.Status.ReadyReplicas != 1 {
				return errors.New("ReadyReplicas is not 1")
			}
			return nil
		}).Should(Succeed())

		Eventually(func() error {
			domainName := test.TestID + ".neco-ops.cybozu-ne.co"
			stdout, _, err := test.ExecAt(test.Boot0, "kubectl", "-n", "internet-egress", "get", "pod", "--selector=run=test-ubuntu", "-o", "json")
			if err != nil {
				return err
			}
			podList := new(corev1.PodList)
			err = json.Unmarshal(stdout, podList)
			if err != nil {
				return err
			}
			if len(podList.Items) != 1 {
				return errors.New("ubuntu pod doesn't exist")
			}
			podName := podList.Items[0].Name
			stdout, stderr, err := test.ExecAt(test.Boot0, "kubectl", "-n", "internet-egress", "exec", podName, "--", "dig", "+noall", "+answer", "@ns-gcp-private.googledomains.com.", domainName)
			if err != nil {
				return fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
			}
			// expected: xxx.neco-ops.cybozu-ne.co. 300 IN A 10.10.10.10
			fields := strings.Fields(string(bytes.TrimSpace(stdout)))
			if len(fields) < 5 || fields[4] != "10.0.5.9" {
				return errors.New("expected IP address is 10.0.5.9, but actual response is " + string(stdout))
			}
			return nil
		}).Should(Succeed())

		_, stderr, err = test.ExecAt(test.Boot0, "kubectl", "-n", "internet-egress", "delete", "deployment", "test-ubuntu")
		Expect(err).NotTo(HaveOccurred(), "stderr: %s", stderr)
	})
}
