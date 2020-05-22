#!/bin/bash

source "/home/tmp-helper.sh"
source "/home/cf-helper.sh"

echo "Prepare CF env for testing"

echo "init"
INSTALL_TIMEOUT=40 #service deploy timeout
branch_name=$(echo $GITHUB_REF | awk -F'/' '{print $3}')
org_name="atlas-test-org"
make_pcf_metadata $INPUT_PCF_URL $INPUT_PCF_USER $INPUT_PCF_PASSWORD

echo "Login. Create ORG and SPACE depended on the branch name"
cf_login
space_name="test-space"-$branch_name
cf create-org $org_name && cf target -o $org_name
cf create-space $space_name && cf target -s $space_name

echo "Create service-broker"
create_atlas_service_broker_from_repo  mongodb-atlas-$branch_name atlas-osb-app-$branch_name

cf enable-service-access mongodb-atlas-aws -p M10 -o $org_name
cf marketplace

create_service aws-atlas-test-instance-$branch_name

echo "Prepare app"
git clone https://github.com/cloudfoundry-samples/spring-music.git
#TODO do we need autoconfiguration (source: vcap) - need to fix?
u=$(cf env test-clean-app | awk '$1 ~ /"uri"\:/{print substr($2, 16, length($2)-17) }')
uname=$(cf env test-clean-app | awk '$1 ~ /username/{print substr($2, 2, length($2)-3) }')
p=$(cf env test-clean-app | awk '$1 ~ /password/{print substr($2, 2, length($2)-3) }')
db="test"
connection="\"mongodb+srv://$uname:$p@$u/$db\""
awk '/generate-ddl: true/ { print; print "  data:"; print "    mongodb:"; print "      uri: "'"${connection}"'""; next }1' spring-music/src/main/resources/application.yml > application_temp | rm spring-music/src/main/resources/application.yml | mv application_temp spring-music/src/main/resources/application.yml
cd spring-music
ls
./gradlew clean assemble
cf push test-app-$branch_name --no-start
cf bind-service test-app aws-atlas-test-instance-$branch_name
cf restart test-app-$branch_name

#we can hide "prepare app" with
#cf push APP_NAME --docker-image [REGISTRY_HOST:PORT/]IMAGE[:TAG] [--docker-username USERNAME] [-c COMMAND] [-f MANIFEST_PATH | --no-manifest] [--no-start] [-i NUM_INSTANCES] [-k DISK] [-m MEMORY] [-t HEALTH_TIMEOUT] [-u (process | port | http)] [--no-route | --random-route | --hostname HOST | --no-hostname] [-d DOMAIN] [--route-path ROUTE_PATH] [--var KEY=VALUE]... [--vars-file VARS_FILE_PATH]...
