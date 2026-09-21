package handler

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShouldPersistRequestPayloadDetail(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		streamErr bool
		errorType string
		want      bool
	}{
		{name: "successful request is omitted", status: http.StatusOK, want: false},
		{name: "successful stream without terminal error is omitted", status: http.StatusOK, want: false},
		{name: "http failure is retained", status: http.StatusBadGateway, want: true},
		{name: "stream terminal failure is retained", status: http.StatusOK, streamErr: true, want: true},
		{name: "semantic error envelope is retained", status: http.StatusOK, errorType: "api_error", want: true},
		{name: "empty status is not treated as failure", status: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := parsedOpsError{StreamFailure: tt.streamErr, ErrorType: tt.errorType}
			require.Equal(t, tt.want, shouldPersistRequestPayloadDetail(tt.status, parsed))
		})
	}
}
