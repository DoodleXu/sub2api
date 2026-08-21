//go:build unit

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeProxyProbeURLs(t *testing.T) {
	t.Parallel()

	got, err := normalizeProxyProbeURLs([]ProbeURLConfig{
		{URL: " https://chatgpt.com/cdn-cgi/trace ", Parser: " CHATGPT-TRACE "},
		{URL: "https://api64.ipify.org?format=json", Parser: "ipify"},
	})
	require.NoError(t, err)
	require.Equal(t, []ProbeURLConfig{
		{URL: "https://chatgpt.com/cdn-cgi/trace", Parser: "chatgpt-trace"},
		{URL: "https://api64.ipify.org?format=json", Parser: "ipify"},
	}, got)
}

func TestNormalizeProxyProbeURLsRejectsInvalidEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		target  ProbeURLConfig
		wantErr string
	}{
		{name: "missing URL", target: ProbeURLConfig{Parser: "ipify"}, wantErr: "url is required"},
		{name: "missing parser", target: ProbeURLConfig{URL: "https://example.com"}, wantErr: "parser is required"},
		{name: "unknown parser", target: ProbeURLConfig{URL: "https://example.com", Parser: "ip_api"}, wantErr: "unsupported parser"},
		{name: "relative URL", target: ProbeURLConfig{URL: "/cdn-cgi/trace", Parser: "chatgpt-trace"}, wantErr: "invalid url"},
		{name: "unsupported scheme", target: ProbeURLConfig{URL: "ftp://example.com/file", Parser: "ipify"}, wantErr: "scheme must be http or https"},
		{name: "userinfo", target: ProbeURLConfig{URL: "https://user:pass@chatgpt.com/cdn-cgi/trace", Parser: "chatgpt-trace"}, wantErr: "userinfo is not allowed"},
		{name: "untrusted host", target: ProbeURLConfig{URL: "https://example.com/ip", Parser: "ipify"}, wantErr: "host \"example.com\" is not allowed"},
		{name: "private literal", target: ProbeURLConfig{URL: "http://127.0.0.1/ip", Parser: "ipify"}, wantErr: "host \"127.0.0.1\" is not allowed"},
		{name: "insecure chatgpt", target: ProbeURLConfig{URL: "http://chatgpt.com/cdn-cgi/trace", Parser: "chatgpt-trace"}, wantErr: "https is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeProxyProbeURLs([]ProbeURLConfig{tt.target})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestProxyProbeHostAllowedRequiresParserSpecificHost(t *testing.T) {
	t.Parallel()

	require.True(t, proxyProbeHostAllowed("ip-api", "ip-api.com"))
	require.True(t, proxyProbeHostAllowed("ipify", "api64.ipify.org"))
	require.True(t, proxyProbeHostAllowed("chatgpt-trace", "chatgpt.com"))
	require.False(t, proxyProbeHostAllowed("ipify", "chatgpt.com"))
	require.False(t, proxyProbeHostAllowed("chatgpt-trace", "api.chatgpt.com"))
}
