library identifier: 'jenkins-shared-library@main', retriever: modernSCM([
    $class: 'GitSCMSource',
    remote: 'http://192.168.15.85/devsecops1/pipeline.git',
    credentialsId: 'lsaid',
    traits: [[$class: 'jenkins.plugins.git.traits.BranchDiscoveryTrait']]
])

pipeline {
    agent any

    options {
        buildDiscarder(logRotator(numToKeepStr: '5', artifactNumToKeepStr: '0'))
        disableConcurrentBuilds()
        timestamps()
        timeout(time: 60, unit: 'MINUTES')
    }

    // =========================================================================
    // ↓↓↓ CHANGE THESE PER PROJECT — everything else stays the same ↓↓↓
    // =========================================================================
    environment {

        // Project identity
        PROJECT_NAME            = 'K8s Dashboard'
        IMAGE_NAME              = 'k8s-dashboard'
        HARBOR_PROJECT          = 'k8s_dashboard'
        REGISTRY_URL            = 'harbor.devops.softnethq.co.tz'
        REGISTRY_CREDENTIALS_ID = 'robot-jenkins'

        // Notifications
        NOTIFICATION_EMAIL = 'lsaid@softnet.co.tz'

        // Source repo
        GIT_REPO_URL       = 'http://192.168.15.85/devsecops1/k8s-dashboard.git'
        GIT_CREDENTIALS_ID = 'lsaid'
        BRANCH_NAME        = 'main'

        // K8s manifest repo
        K8S_MANIFEST_REPO_URL       = 'http://192.168.15.85/kubernetes-manifest/k8s-dashboard-manifest.git'
        K8S_MANIFEST_CREDENTIALS_ID = 'lsaid'
        K8S_MANIFEST_BRANCH         = 'main'
        K8S_MANIFEST_PATHS          = 'k8s/02-deployment.yaml'

        // Build tool: maven | npm | node | next | go | gradle | dotnet
        BUILD_TOOL = 'go'

        // App config
        APP_TIMEZONE = 'Africa/Dar_es_Salaam'

        // DefectDojo
        DEFECTDOJO_URL           = 'https://defectdojo.devops.softnethq.co.tz'
        DEFECTDOJO_ENGAGEMENT_ID = '21'

        // Dependency-Track — SBOM upload
        DEPENDENCY_TRACK_URL = 'https://dependencytrack.devops.softnethq.co.tz'

        // DAST — staging URL of this service on K8s cluster (leave empty to skip)
        // STAGING_URL = 'https://your-service.staging.k8s.softnethq.co.tz'  // set to enable DAST

        // Auto-populated — do not edit
        GIT_COMMIT     = sh(script: 'git rev-parse HEAD 2>/dev/null || echo unknown', returnStdout: true).trim()
        GIT_AUTHOR     = sh(script: 'git log -1 --pretty=format:"%an" 2>/dev/null || echo unknown', returnStdout: true).trim()
        APP_VERSION    = "1.0.${env.BUILD_NUMBER}"
        BUILD_DATE_UTC = sh(script: "date -u +'%Y-%m-%dT%H:%M:%SZ'", returnStdout: true).trim()
    }
    // =========================================================================
    // ↑↑↑ END OF PER-PROJECT SECTION ↑↑↑
    // =========================================================================

    stages {

        stage('Checkout and Git Info') {
            steps {
                script {
                    checkoutAndGitInfo(
                        repo: env.GIT_REPO_URL,
                        credentialsId: env.GIT_CREDENTIALS_ID,
                        branch: env.BRANCH_NAME
                    )
                }
            }
        }

        stage('Send Start Notification') {
            steps {
                script {
                    sendStartNotification(
                        subject: "🚀 Pipeline Started: ${env.JOB_NAME} #${env.BUILD_NUMBER}",
                        recipients: env.NOTIFICATION_EMAIL,
                        triggeredBy: detectBuildTrigger()
                    )
                }
            }
        }

        stage('Build Artifact') {
            steps {
                script { buildArtifact(command: 'go build -o app ./cmd/server') }
            }
        }

        // stage('Unit Tests') {
        //     steps {
        //         script { unitTests() }
        //     }
        // }

        stage('SonarQube Analysis') {
            steps {
                script {
                    sonarSast(
                        sonarServer: 'SonarQube Server',
                        projectKey: "${env.IMAGE_NAME}",
                        projectName: "${env.PROJECT_NAME}",
                        waitForQualityGate: false,
                        timeoutMinutes: 5
                    )
                }
            }
        }

        stage('Dependency Check') {
            steps {
                script {
                    parallel(
                        "OWASP Dependency Check": {
                            owaspDependencyCheck(failOnCVSS: 0)
                        },
                        "OSV Scanner": {
                            osvScanner(failOnCritical: false)
                        }
                    )
                }
            }
        }

        stage('Vulnerability Scan - Docker') {
            steps {
                script { vulnScanDocker() }
            }
        }

        stage('Build Docker Image and Publish') {
            steps {
                script {
                    def result = buildDockerImageAndPush(
                        imageName:              env.IMAGE_NAME,
                        imageTag:               env.BUILD_NUMBER,
                        harborProject:          env.HARBOR_PROJECT,
                        registryUrl:            env.REGISTRY_URL,
                        registryCredentialsId:  env.REGISTRY_CREDENTIALS_ID,
                        pushToRegistry:         true,
                        buildArgs: [
                            GIT_AUTHOR  : env.GIT_AUTHOR,
                            GIT_COMMIT  : env.GIT_COMMIT,
                            BUILD_DATE  : new Date().format("yyyy-MM-dd'T'HH:mm:ss'Z'", TimeZone.getTimeZone('UTC')),
                            VERSION     : "1.0.${env.BUILD_NUMBER}",
                            APP_TIMEZONE: env.APP_TIMEZONE,
                            APP_NAME    : env.PROJECT_NAME
                        ]
                    )
                    env.FINAL_IMAGE_NAME = result.localImageName
                }
            }
        }

        stage('Sign Image') {
            steps {
                script {
                    signImage(
                        registryUrl:   env.REGISTRY_URL,
                        harborProject: env.HARBOR_PROJECT,
                        imageName:     env.IMAGE_NAME,
                        imageTag:      env.BUILD_NUMBER
                    )
                }
            }
        }

        stage('Generate & Upload SBOM') {
            steps {
                script {
                    generateSbom(
                        projectName:    env.IMAGE_NAME,
                        projectVersion: env.APP_VERSION
                    )
                }
            }
        }

        stage('Vulnerability Scan - Application Image') {
            steps {
                script { vulnScanApplicationImage() }
            }
        }

        stage('Publish Security Results') {
            steps {
                script { publishToDefectDojo() }
            }
        }

        stage('k8s Manifest Update') {
            steps {
                script {
                    k8sManifestScanAndUpdate()
                }
            }
        }

        stage('DAST Scan') {
            steps {
                script {
                    dastScan(
                        stagingUrl:  env.STAGING_URL,
                        scanType:    'baseline',
                        waitSeconds: 90
                    )
                }
            }
        }
    }

    post {

        always {
            script {
                if (fileExists('target/dependency-check-report.xml')) {
                    dependencyCheckPublisher(
                        pattern: 'target/dependency-check-report.xml',
                        failedTotalCritical: 0,
                        unstableTotalHigh: 10
                    )
                } else {
                    echo "ℹ️  No dependency-check-report.xml — skipping (non-Maven project)"
                }
            }
        }

        success {
            script {
                sendSuccessNotification(
                    recipients: env.NOTIFICATION_EMAIL,
                    triggeredBy: detectBuildTrigger()
                )
            }
        }

        failure {
            script {
                sendFailureNotification(
                    recipients: env.NOTIFICATION_EMAIL,
                    triggeredBy: detectBuildTrigger()
                )
            }
        }

        cleanup {
            cleanWs(
                cleanWhenSuccess:  true,
                cleanWhenFailure:  false,
                cleanWhenAborted:  true,
                notFailBuild:      true
            )
        }
    }
}
