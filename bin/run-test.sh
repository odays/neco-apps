#!/bin/sh

. ${NECO_DIR}/bin/env
TARGET=${TARGET:-test-apps}
BASE_BRANCH=${BASE_BRANCH:-master}

cat >run.sh <<EOF
#!/bin/sh -ex
# Run test
GOPATH=\$HOME/go
GO111MODULE=on
export GOPATH GO111MODULE
PATH=/usr/local/go/bin:\$GOPATH/bin:\$PATH
export PATH
NECO_DIR=\$HOME/go/src/github.com/${CIRCLE_PROJECT_USERNAME}/neco
export NECO_DIR
git clone https://github.com/${CIRCLE_PROJECT_USERNAME}/${CIRCLE_PROJECT_REPONAME} \$HOME/go/src/github.com/${CIRCLE_PROJECT_USERNAME}/${CIRCLE_PROJECT_REPONAME}
cd \$HOME/go/src/github.com/${CIRCLE_PROJECT_USERNAME}/${CIRCLE_PROJECT_REPONAME}
git checkout -qf ${CIRCLE_SHA1}

cd test
cp /home/cybozu/account.json ./
make setup
make $TARGET COMMIT_ID=${CIRCLE_SHA1} BASE_BRANCH=${BASE_BRANCH}
EOF
chmod +x run.sh

# Clean old CI files
$GCLOUD compute scp --zone=${ZONE} run.sh account.json cybozu@${INSTANCE_NAME}:
$GCLOUD compute ssh --zone=${ZONE} cybozu@${INSTANCE_NAME} --command="sudo -H /home/cybozu/run.sh"

exit $?
