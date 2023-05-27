package tools

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"

	// "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/codeartifact"
	"github.com/aws/aws-sdk-go-v2/service/codeartifact/types"
)

type CodeArtifactInfoStruct struct {
	Region     string
	Owner      string
	Domain     string
	Repository string
}

type CodeArtifactAuthInfoStruct struct {
	Url                string
	AuthorizationToken string
	LastAuth           time.Time
}

var CodeArtifactInfoMap = make(map[string]*CodeArtifactInfoStruct)
var CodeArtifactInfoMapMutex = sync.RWMutex{}

var CodeArtifactInfoDev = &CodeArtifactInfoStruct{}
var CodeArtifactInfoStage = &CodeArtifactInfoStruct{}
var CodeArtifactInfoProd = &CodeArtifactInfoStruct{}

var CodeArtifactAuthInfoMap = make(map[string]*CodeArtifactAuthInfoStruct)
var CodeArtifactAuthInfoMapMutex = sync.RWMutex{}

func Init() {
	CodeArtifactAuthInfoMap["dev"] = &CodeArtifactAuthInfoStruct{}
	CodeArtifactAuthInfoMap["stage"] = &CodeArtifactAuthInfoStruct{}
	CodeArtifactAuthInfoMap["prod"] = &CodeArtifactAuthInfoStruct{}

	CodeArtifactInfoDev.Region = os.Getenv("AWS_REGION")
	CodeArtifactInfoDev.Owner = os.Getenv("BE_CODEARTIFACT_DEV_OWNER")
	CodeArtifactInfoDev.Domain = os.Getenv("BE_CODEARTIFACT_DEV_DOMAIN")
	CodeArtifactInfoDev.Repository = os.Getenv("BE_CODEARTIFACT_DEV_REPOSITORY")

	CodeArtifactInfoStage.Region = os.Getenv("AWS_REGION")
	CodeArtifactInfoStage.Owner = os.Getenv("BE_CODEARTIFACT_STAGE_OWNER")
	CodeArtifactInfoStage.Domain = os.Getenv("BE_CODEARTIFACT_STAGE_DOMAIN")
	CodeArtifactInfoStage.Repository = os.Getenv("BE_CODEARTIFACT_STAGE_REPOSITORY")

	CodeArtifactInfoProd.Region = os.Getenv("AWS_REGION")
	CodeArtifactInfoProd.Owner = os.Getenv("BE_CODEARTIFACT_PROD_OWNER")
	CodeArtifactInfoProd.Domain = os.Getenv("BE_CODEARTIFACT_PROD_DOMAIN")
	CodeArtifactInfoProd.Repository = os.Getenv("BE_CODEARTIFACT_PROD_REPOSITORY")

	CodeArtifactInfoMap["dev"] = CodeArtifactInfoDev
	CodeArtifactInfoMap["stage"] = CodeArtifactInfoStage
	CodeArtifactInfoMap["prod"] = CodeArtifactInfoProd
}

// Authenticate performs the authentication against CodeArtifact and caches the credentials
func Authenticate(env string) {
	log.Printf("Authenticating against %s CodeArtifact", env)

	// awsAccessKeyId := aws.String(os.Getenv("AWS_ACCESS_KEY_ID"))
	// awsSecretAccessKey := aws.String(os.Getenv("AWS_SECRET_ACCESS_KEY"))
	// awsSessionToken := aws.String(os.Getenv("AWS_SESSION_TOKEN"))

	// codeartifactRegion := aws.String(CodeArtifactInfoMap[env].Region)
	CodeArtifactInfoMapMutex.RLock()
	codeartifactOwner := aws.String(CodeArtifactInfoMap[env].Owner)
	codeartifactDomain := aws.String(CodeArtifactInfoMap[env].Domain)
	codeartifactRepository := aws.String(CodeArtifactInfoMap[env].Repository)
	CodeArtifactInfoMapMutex.RUnlock()

	// Authenticate against CodeArtifact
	// cfg, cfgErr := config.LoadDefaultConfig(context.TODO(), config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(aws.ToString(awsAccessKeyId), aws.ToString(awsSecretAccessKey), aws.ToString(awsSessionToken))), config.WithRegion(aws.ToString(codeartifactRegion)))
	cfg, cfgErr := config.LoadDefaultConfig(context.TODO())
	if cfgErr != nil {
		log.Fatalf("unable to load SDK config, %v", cfgErr)
	}
	svc := codeartifact.NewFromConfig(cfg)

	// Resolve Package Format from the environment variable (defaults to npm)
	codeartifactTypeS, found := os.LookupEnv("BE_CODEARTIFACT_TYPE")
	if !found || codeartifactTypeS == "" {
		codeartifactTypeS = "npm"
	}
	var codeartifactTypeT types.PackageFormat
	if codeartifactTypeS == "pypi" {
		codeartifactTypeT = types.PackageFormatPypi
	} else if codeartifactTypeS == "maven" {
		codeartifactTypeT = types.PackageFormatMaven
	} else if codeartifactTypeS == "npm" {
		codeartifactTypeT = types.PackageFormatNpm
	} else if codeartifactTypeS == "nuget" {
		codeartifactTypeT = types.PackageFormatNuget
	}

	// Create the input for the CodeArtifact API
	authInput := &codeartifact.GetAuthorizationTokenInput{
		DurationSeconds: aws.Int64(3600),
		Domain:          codeartifactDomain,
		DomainOwner:     codeartifactOwner,
	}

	authResp, authErr := svc.GetAuthorizationToken(context.TODO(), authInput)
	if authErr != nil {
		log.Printf("GetAuthorizationToken Response %v", authResp)
		log.Fatalf("unable to get authorization token, %v", authErr)
	}
	log.Printf("Authorization successful")

	CodeArtifactAuthInfoMapMutex.Lock()
	CodeArtifactAuthInfoMap[env].AuthorizationToken = *authResp.AuthorizationToken
	CodeArtifactAuthInfoMap[env].LastAuth = time.Now()
	CodeArtifactAuthInfoMapMutex.Unlock()

	// Get the URL for the CodeArtifact Service
	urlInput := &codeartifact.GetRepositoryEndpointInput{
		Domain:      codeartifactDomain,
		Format:      codeartifactTypeT,
		Repository:  codeartifactRepository,
		DomainOwner: codeartifactOwner,
	}

	urlResp, urlErr := svc.GetRepositoryEndpoint(context.TODO(), urlInput)
	if urlErr != nil {
		log.Fatalf("unable to get repository endpoint, %v", urlErr)
	}

	CodeArtifactAuthInfoMapMutex.Lock()
	CodeArtifactAuthInfoMap[env].Url = *urlResp.RepositoryEndpoint
	CodeArtifactAuthInfoMapMutex.Unlock()

	CodeArtifactAuthInfoMapMutex.RLock()
	log.Printf("Requests for %s will now be proxied to %s", env, CodeArtifactAuthInfoMap[env].Url)
	CodeArtifactAuthInfoMapMutex.RUnlock()
}

// CheckReauth checks if we have not yet authenticated, or need to authenticate within the next 15 minutes
func CheckReauth(_env string) {
	for {
		CodeArtifactAuthInfoMapMutex.RLock()
		authToken := CodeArtifactAuthInfoMap[_env].AuthorizationToken
		timeSince := time.Since(CodeArtifactAuthInfoMap[_env].LastAuth).Minutes()
		CodeArtifactAuthInfoMapMutex.RUnlock()

		// Panic and shut down the proxy if we couldn't reauthenticate within the 15 minute window for some reason.
		if timeSince > float64(60) {
			log.Panic("Was unable to re-authenticate prior to our token expiring, shutting down proxy...")
		}

		if authToken == "" || timeSince > float64(45) {
			log.Printf("%f minutes until the CodeArtifact token expires, attempting a reauth.", 60-timeSince)
			Authenticate(_env)
		}

		// Sleep for 15 seconds for the next check
		time.Sleep(15 * time.Second)
	}
}
