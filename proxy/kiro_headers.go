package proxy

// Builds the HTTP headers that make requests look like they come from a real
// Kiro IDE client (User-Agent / x-amz-user-agent spoofing). The upstream AWS
// services gate on these strings, so the SDK versions and format must match
// what the genuine client sends.

import (
	"fmt"
	"kiro-go/config"
	"net/http"
)

// aws-sdk-js versions reported per API surface: the streaming
// (codewhispererstreaming) and runtime (codewhispererruntime) endpoints
// advertise different SDK versions, mirroring the real client.
const (
	kiroStreamingSDKVersion = "1.0.34"
	kiroRuntimeSDKVersion   = "1.0.0"
)

// kiroHeaderValues holds the spoofed header values for a single request.
type kiroHeaderValues struct {
	UserAgent    string
	AmzUserAgent string
	Host         string
}

// buildStreamingHeaderValues builds headers for the streaming
// (generateAssistantResponse) endpoint.
func buildStreamingHeaderValues(account *config.Account, host string) kiroHeaderValues {
	return buildKiroHeaderValues(account, host, "codewhispererstreaming", kiroStreamingSDKVersion, "m/E")
}

// buildRuntimeHeaderValues builds headers for the runtime REST endpoints
// (usage limits, user info, model listing).
func buildRuntimeHeaderValues(account *config.Account, host string) kiroHeaderValues {
	return buildKiroHeaderValues(account, host, "codewhispererruntime", kiroRuntimeSDKVersion, "m/N,E")
}

// buildKiroHeaderValues assembles the User-Agent and x-amz-user-agent strings.
// The account's MachineId, when present, is appended so requests from the same
// account share a stable client fingerprint.
func buildKiroHeaderValues(account *config.Account, host, apiName, sdkVersion, mode string) kiroHeaderValues {
	clientCfg := config.GetKiroClientConfig()
	machineID := ""
	if account != nil {
		machineID = account.MachineId
	}

	userAgent := fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s lang/js md/nodejs#%s api/%s#%s %s KiroIDE-%s",
		sdkVersion,
		clientCfg.SystemVersion,
		clientCfg.NodeVersion,
		apiName,
		sdkVersion,
		mode,
		clientCfg.KiroVersion,
	)
	amzUserAgent := fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s", sdkVersion, clientCfg.KiroVersion)
	if machineID != "" {
		userAgent += "-" + machineID
		amzUserAgent += "-" + machineID
	}

	return kiroHeaderValues{
		UserAgent:    userAgent,
		AmzUserAgent: amzUserAgent,
		Host:         host,
	}
}

// applyKiroBaseHeaders sets the bearer token, spoofed user-agent headers, the
// telemetry opt-out flag, and the Host override onto an outbound request.
func applyKiroBaseHeaders(req *http.Request, account *config.Account, values kiroHeaderValues) {
	if account != nil && account.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+account.AccessToken)
	}
	req.Header.Set("User-Agent", values.UserAgent)
	req.Header.Set("x-amz-user-agent", values.AmzUserAgent)
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	if values.Host != "" {
		req.Host = values.Host
	}
}
