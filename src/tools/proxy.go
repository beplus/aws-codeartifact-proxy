package tools

import (
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

var originalUrlResolver = make(map[string]*url.URL)
var originalUrlResolverMutex = sync.RWMutex{}

var tokenToEnvMap = make(map[string]string)
var tokenToEnvMapMutex = sync.RWMutex{}

// ProxyRequestHandler intercepts requests to CodeArtifact and add the Authorization header + correct Host header
func ProxyRequestHandler(p *httputil.ReverseProxy) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// Store the original host header for each request
		originalUrlResolverMutex.Lock()
		originalUrlResolver[r.RemoteAddr] = r.URL
		originalUrlResolver[r.RemoteAddr].Host = r.Host
		originalUrlResolver[r.RemoteAddr].Scheme = r.URL.Scheme
		originalUrlResolverMutex.Unlock()

		authToken := r.Header.Get("Authorization")
		splitToken := strings.Split(authToken, "Bearer ")
		authToken = splitToken[1]

		tokenToEnvMapMutex.RLock()
		authEnvReq := tokenToEnvMap[authToken]
		tokenToEnvMapMutex.RUnlock()

		log.Printf("%s → Received authToken %s for %s environment", authEnvReq, authToken, authEnvReq)
		CodeArtifactAuthInfoMapMutex.RLock()
		log.Printf("%s → Proxying request to URL %s", authEnvReq, CodeArtifactAuthInfoMap[authEnvReq].Url)
		CodeArtifactAuthInfoMapMutex.RUnlock()

		originalUrlResolverMutex.Lock()
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			originalUrlResolver[r.RemoteAddr].Scheme = "https"
		} else {
			originalUrlResolver[r.RemoteAddr].Scheme = "http"
		}
		originalUrlResolverMutex.Unlock()

		// Override the Host header with the CodeArtifact Host
		CodeArtifactAuthInfoMapMutex.RLock()
		u, _ := url.Parse(CodeArtifactAuthInfoMap[authEnvReq].Url)
		CodeArtifactAuthInfoMapMutex.RUnlock()
		log.Printf("%s → Host: from %s to %s", authEnvReq, r.Host, u.Host)
		r.Host = u.Host

		// Set the Authorization header with the CodeArtifact Authorization Token
		CodeArtifactAuthInfoMapMutex.RLock()
		r.SetBasicAuth("aws", CodeArtifactAuthInfoMap[authEnvReq].AuthorizationToken)

		log.Printf("%s → Request: %s %s \"%s\" \"%s\"", authEnvReq, r.RemoteAddr, r.Method, r.URL.RequestURI(), r.UserAgent())
		log.Printf("%s → Sending request to %s%s", authEnvReq, strings.Trim(CodeArtifactAuthInfoMap[authEnvReq].Url, "/"), r.URL.RequestURI())
		CodeArtifactAuthInfoMapMutex.RUnlock()

		p.ServeHTTP(w, r)
	}
}

func ProxyResponseHandler(resEnv string) func(*http.Response) error {
	return func(r *http.Response) error {
		log.Printf("%s → Received response from %s", resEnv, r.Request.URL.String())
		log.Printf("%s → Response: %s \"%s\" %d \"%s\" \"%s\"", resEnv, r.Request.RemoteAddr, r.Request.Method, r.StatusCode, r.Request.RequestURI, r.Request.UserAgent())

		contentType := r.Header.Get("Content-Type")

		originalUrlResolverMutex.Lock()
		originalUrl := originalUrlResolver[r.Request.RemoteAddr]
		delete(originalUrlResolver, r.Request.RemoteAddr)
		originalUrlResolverMutex.Unlock()

		CodeArtifactAuthInfoMapMutex.RLock()
		u, _ := url.Parse(CodeArtifactAuthInfoMap[resEnv].Url)
		CodeArtifactAuthInfoMapMutex.RUnlock()
		hostname := u.Host + ":443"

		if r.StatusCode == 404 {
			return nil
		}

		// The request flows like this:
		// 1. Original request from NPM:
		//    - https://npm.beplus.cloud/@bepluscloud-aws/components
		//    Proxy rewrites to:
		//    - https://bepluscloud-dev-379737076335.d.codeartifact.us-east-1.amazonaws.com/npm/bepluscloud-dev/@bepluscloud-aws/components
		// 2. Next request is based on the response from the previous one, and goes to:
		//    - https://npm.beplus.cloud/@bepluscloud-aws/components/-/components-0.66.0.tgz
		//    Proxy rewrites to:
		//    - https://bepluscloud-dev-379737076335.d.codeartifact.us-east-1.amazonaws.com/npm/bepluscloud-dev/@bepluscloud-aws/components/-/components-0.66.0.tgz
		// 3. Next request is based on the response from the previous one, and goes to:
		//    - https://assets-XYZ-REGION.s3.amazonaws.com/HASH/UUID1/UUID2
		//    and that should NOT be rewritten, but fulfilled.
		// It doesn't work with the following code, but works fine when the code is commented.
		//

		// Rewrite the 301 to point from CodeArtifact URL to the proxy instead.
		// if r.StatusCode == 301 || r.StatusCode == 302 {
		// 	location, _ := r.Location()

		// 	location.Host = originalUrl.Host
		// 	location.Scheme = originalUrl.Scheme
		// 	location.Path = strings.Replace(location.Path, u.Path, "", 1)

		// 	r.Header.Set("Location", location.String())
		// }

		// Do some quick fixes to the HTTP response for NPM install requests
		if strings.Contains(r.Request.UserAgent(), "npm") {

			// Respond to only requests that respond with JSON
			// There might eventually be additional headers i don't know about?
			if !strings.Contains(contentType, "application/json") && !strings.Contains(contentType, "application/vnd.npm.install-v1+json") {
				return nil
			}

			var body io.ReadCloser

			if r.Header.Get("Content-Encoding") == "gzip" {
				body, _ = gzip.NewReader(r.Body)
				r.Header.Del("Content-Encoding")
			} else {
				body = r.Body
			}

			// replace any instances of the CodeArtifact URL with the local URL
			oldContentResponse, _ := ioutil.ReadAll(body)
			oldContentResponseStr := string(oldContentResponse)

			CodeArtifactAuthInfoMapMutex.RLock()

			resolvedHostname := strings.Replace(CodeArtifactAuthInfoMap[resEnv].Url, u.Host, hostname, -1)
			newUrl := fmt.Sprintf("%s://%s/", originalUrl.Scheme, originalUrl.Host)

			newResponseContent := strings.Replace(oldContentResponseStr, resolvedHostname, newUrl, -1)
			newResponseContent = strings.Replace(newResponseContent, CodeArtifactAuthInfoMap[resEnv].Url, newUrl, -1)

			CodeArtifactAuthInfoMapMutex.RUnlock()

			r.Body = ioutil.NopCloser(strings.NewReader(newResponseContent))
			r.ContentLength = int64(len(newResponseContent))
			r.Header.Set("Content-Length", strconv.Itoa(len(newResponseContent)))
		}

		return nil
	}

}

//

var (
	hostTarget = map[string]string{
		"dev":   "dev",
		"stage": "stage",
		"prod":  "prod",
	}
	hostProxy map[string]*httputil.ReverseProxy = map[string]*httputil.ReverseProxy{}
)

type baseHandle struct{}

func (h *baseHandle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authToken := r.Header.Get("Authorization")
	if len(authToken) > 0 {
		splitToken := strings.Split(authToken, "Bearer ")
		authToken = splitToken[1]

		tokenToEnvMapMutex.RLock()
		proxyEnv := tokenToEnvMap[authToken]
		tokenToEnvMapMutex.RUnlock()

		if fn, ok := hostProxy[proxyEnv]; ok {
			ProxyRequestHandler(fn)(w, r)
			return
		}

		if target, ok := hostTarget[proxyEnv]; ok {
			CodeArtifactAuthInfoMapMutex.RLock()
			remoteUrl, err := url.Parse(CodeArtifactAuthInfoMap[target].Url)
			CodeArtifactAuthInfoMapMutex.RUnlock()
			if err != nil {
				log.Println("target parse fail:", err)
				return
			}

			proxy := httputil.NewSingleHostReverseProxy(remoteUrl)
			proxy.ModifyResponse = ProxyResponseHandler(proxyEnv)

			hostProxy[proxyEnv] = proxy
			ProxyRequestHandler(proxy)(w, r)
			return
		}
	}

	w.Write([]byte("403: Forbidden"))
}

// ProxyInit initialises the CodeArtifact proxy and starts the HTTP listener
func ProxyInit() {
	tokenToEnvMapMutex.Lock()
	tokenToEnvMap["1cd67aa3-76a2-45dd-ab86-c27a6da0591c"] = "prod"
	tokenToEnvMap["fce9ba15-7ce0-42db-8c9b-40eaf96e9b2c"] = "stage"
	tokenToEnvMap["bf9d88e0-e97e-45e9-a492-766155ae69ac"] = "dev"
	tokenToEnvMapMutex.Unlock()

	h := &baseHandle{}
	http.Handle("/", h)

	server := &http.Server{
		Addr:    ":8080",
		Handler: h,
	}
	log.Fatal(server.ListenAndServe())

	//

	// remote, err := url.Parse(CodeArtifactAuthInfoMap["dev"].Url)
	// if err != nil {
	// 	panic(err)
	// }

	// proxy := httputil.NewSingleHostReverseProxy(remote)

	// proxy.ModifyResponse = ProxyResponseHandler()

	// http.HandleFunc("/", ProxyRequestHandler(proxy))
	// err = http.ListenAndServe(":8080", nil)
	// if err != nil {
	// 	panic(err)
	// }
}
