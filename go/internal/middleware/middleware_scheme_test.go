package middleware

import (
	"bytes"
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoggingRecordsScheme(t *testing.T) {
	for _, tc := range []struct {
		name string
		tls  bool
		want string
	}{
		{"clair", false, "scheme=http"},
		{"tls", true, "scheme=https"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			h := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

			req := httptest.NewRequest(http.MethodGet, "/boot.img", nil)
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			h.ServeHTTP(httptest.NewRecorder(), req)

			if got := buf.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("log sans %q : %s", tc.want, got)
			}
		})
	}
}
