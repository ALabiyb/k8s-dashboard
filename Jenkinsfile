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

        PROJECT_NAME            = 'K8s Dashboard'
        IMAGE_NAME              = 'k8s-dashboard'
        HARBOR_PROJECT          = 'k8s_dashboard'
        REGISTRY_URL            = 'harbor.devops.softnethq.co.tz'
        REGISTRY_CREDENTIALS_ID = 'robot-jenkins'

        NOTIFICATION_EMAIL = 'lsaid@softnet.co.tz'

        GIT_REPO_URL       = 'http://192.168.15.85/devsecops1/k8s-dashboard.git'
        GIT_CREDENTIALS_ID = 'lsaid'

        K8S_MANIFEST_REPO_URL       = 'http://192.168.15.85/kubernetes-manifest/k8s-dashboard-manifest.git'
        K8S_MANIFEST_CREDENTIALS_ID = 'lsaid'
        K8S_MANIFEST_BRANCH         = 'dev'
        K8S_MANIFEST_UAT_BRANCH     = 'uat'
        K8S_MANIFEST_PROD_BRANCH    = 'prod'
        K8S_MANIFEST_PATHS          = 'k8s/02-deployment.yaml'

        BUILD_TOOL   = 'go'
        APP_TIMEZONE = 'Africa/Dar_es_Salaam'

        DEFECTDOJO_URL           = 'https://defectdojo.devops.softnethq.co.tz'
        DEFECTDOJO_ENGAGEMENT_ID = '20'
        DEPENDENCY_TRACK_URL     = 'https://dependencytrack.devops.softnethq.co.tz'

        // Auto-populated — do not edit
        GIT_COMMIT      = sh(script: 'git rev-parse HEAD 2>/dev/null || echo unknown', returnStdout: true).trim()
        GIT_AUTHOR      = sh(script: 'git log -1 --pretty=format:"%an" 2>/dev/null || echo unknown', returnStdout: true).trim()
        APP_VERSION     = "1.0.${env.BUILD_NUMBER}"
        BUILD_DATE_UTC  = sh(script: "date -u +'%Y-%m-%dT%H:%M:%SZ'", returnStdout: true).trim()
        RELEASE_VERSION = sh(script: "cat VERSION 2>/dev/null || echo ${env.APP_VERSION}", returnStdout: true).trim()

        // The one immutable build tag — every environment references these same bytes.
        IMMUTABLE_TAG = "main-${env.GIT_COMMIT.take(7)}"
    }
    // =========================================================================
    // ↑↑↑ END OF PER-PROJECT SECTION ↑↑↑
    // =========================================================================

    stages {

        // ── Shared prelude (all branches) ────────────────────────────────────
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

        // ╔════════════════════════════════════════════════════════════════════╗
        // ║ BUILD ONCE — these stages run ONLY on `main`.                     ║
        // ║ They produce a single image tagged main-<sha> and record it on    ║
        // ║ the manifest `main` branch. No ArgoCD app watches manifest main   ║
        // ║ so nothing deploys here — it is the "latest good build" source.   ║
        // ╚════════════════════════════════════════════════════════════════════╝

        stage('Build Artifact') {
            when { branch 'main' }
            steps {
                script { buildArtifact(command: 'go build -o app ./cmd/server') }
            }
        }

        stage('SonarQube Analysis') {
            when { branch 'main' }
            steps {
                script {
                    sonarSast(
                        sonarServer:       'SonarQube Server',
                        projectKey:        "${env.IMAGE_NAME}",
                        projectName:       "${env.PROJECT_NAME}",
                        waitForQualityGate: false,
                        timeoutMinutes:    5
                    )
                }
            }
        }

        stage('Dependency Check') {
            when { branch 'main' }
            steps {
                script {
                    parallel(
                        "OWASP Dependency Check": { owaspDependencyCheck(failOnCVSS: 0) },
                        "OSV Scanner":            { osvScanner(failOnCritical: false) }
                    )
                }
            }
        }

        stage('Vulnerability Scan - Docker') {
            when { branch 'main' }
            steps {
                script { vulnScanDocker() }
            }
        }

        stage('Build Docker Image and Publish') {
            when { branch 'main' }
            steps {
                script {
                    def result = buildDockerImageAndPush(
                        imageName:             env.IMAGE_NAME,
                        imageTag:              env.IMMUTABLE_TAG,   // main-<sha>, not build number
                        harborProject:         env.HARBOR_PROJECT,
                        registryUrl:           env.REGISTRY_URL,
                        registryCredentialsId: env.REGISTRY_CREDENTIALS_ID,
                        pushToRegistry:        true,
                        buildArgs: [
                            GIT_AUTHOR  : env.GIT_AUTHOR,
                            GIT_COMMIT  : env.GIT_COMMIT,
                            BUILD_DATE  : new Date().format("yyyy-MM-dd'T'HH:mm:ss'Z'", TimeZone.getTimeZone('UTC')),
                            VERSION     : env.RELEASE_VERSION,
                            APP_TIMEZONE: env.APP_TIMEZONE,
                            APP_NAME    : env.PROJECT_NAME
                        ]
                    )
                    env.FINAL_IMAGE_NAME = result.localImageName
                }
            }
        }

        stage('Sign Image') {
            when { branch 'main' }
            steps {
                script {
                    signImage(
                        registryUrl:   env.REGISTRY_URL,
                        harborProject: env.HARBOR_PROJECT,
                        imageName:     env.IMAGE_NAME,
                        imageTag:      env.IMMUTABLE_TAG
                    )
                }
            }
        }

        stage('Generate & Upload SBOM') {
            when { branch 'main' }
            steps {
                script { generateSbom(projectName: env.IMAGE_NAME) }
            }
        }

        stage('Vulnerability Scan - Application Image') {
            when { branch 'main' }
            steps {
                script { vulnScanApplicationImage() }
            }
        }

        stage('Publish Security Results') {
            when { branch 'main' }
            steps {
                script { publishToDefectDojo() }
            }
        }

        stage('DAST Scan') {
            when { branch 'main' }
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

        stage('Record build on manifest main') {
            when { branch 'main' }
            steps {
                script {
                    // Pin main-<sha> onto manifest `main`. No ArgoCD app watches it —
                    // this records the latest good build; promote stages below deploy it.
                    k8sManifestScanAndUpdate(
                        branch:   'main',
                        imageTag: env.IMMUTABLE_TAG
                    )
                }
            }
        }

        // ╔════════════════════════════════════════════════════════════════════╗
        // ║ PROMOTE — no build. Copy the tag pinned on manifest `main` into   ║
        // ║ the target environment's manifest branch. ArgoCD syncs the same   ║
        // ║ image bytes that passed main's full security gate.                ║
        // ╚════════════════════════════════════════════════════════════════════╝

        stage('Promote to dev') {
            when { branch 'dev' }
            steps {
                script {
                    // No rebuild — promotes the tag already on manifest/main as-is.
                    promoteImage(fromBranch: 'main', toBranch: env.K8S_MANIFEST_BRANCH)
                }
            }
        }

        stage('Promote to uat') {
            when { branch 'uat' }
            steps {
                script {
                    // Re-tag the same digest as uat-<sha> + re-attest env=uat for Kyverno.
                    promoteImage(
                        fromBranch:  'main',
                        toBranch:    env.K8S_MANIFEST_UAT_BRANCH,
                        reTagPrefix: 'uat'
                    )
                }
            }
        }

        stage('Promote to prod') {
            when { branch 'prod' }
            steps {
                script {
                    // Approve-only gate: email + typed-version confirmation.
                    // mode:'promote' means productionApproval does NOT update the manifest —
                    // promoteImage() does that after approval.
                    productionApproval(
                        releaseVersion: env.RELEASE_VERSION,
                        recipients:     env.NOTIFICATION_EMAIL,
                        timeoutMinutes: 30,
                        mode:           'promote'
                    )
                    // Re-tag the same digest as <VERSION> + re-attest env=prod.
                    promoteImage(
                        fromBranch: 'main',
                        toBranch:   env.K8S_MANIFEST_PROD_BRANCH,
                        reTagAs:    env.RELEASE_VERSION
                    )
                }
            }
        }
    }

    post {
        always {
            script {
                if (env.BRANCH_NAME == 'main' && fileExists('target/dependency-check-report.xml')) {
                    dependencyCheckPublisher(
                        pattern:              'target/dependency-check-report.xml',
                        failedTotalCritical:  0,
                        unstableTotalHigh:    10
                    )
                }
            }
        }
        success {
            script {
                sendSuccessNotification(
                    recipients:  env.NOTIFICATION_EMAIL,
                    triggeredBy: detectBuildTrigger()
                )
            }
        }
        unstable {
            script {
                sendSuccessNotification(
                    recipients:  env.NOTIFICATION_EMAIL,
                    triggeredBy: detectBuildTrigger()
                )
            }
        }
        failure {
            script {
                sendFailureNotification(
                    recipients:  env.NOTIFICATION_EMAIL,
                    triggeredBy: detectBuildTrigger()
                )
            }
        }
        cleanup {
            cleanWs(
                cleanWhenSuccess: true,
                cleanWhenFailure: false,
                cleanWhenAborted: true,
                notFailBuild:     true
            )
        }
    }
}
