package objectstore

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestS3ProtocolConditionalPublicationAndExistingObjectVerification(t *testing.T) {
	body := []byte("protocol-level immutable evidence")
	descriptor := descriptorFor(body, "text/plain")
	digestBase64 := base64.StdEncoding.EncodeToString(descriptor.SHA256[:])
	digestHex := hex.EncodeToString(descriptor.SHA256[:])
	var mutex sync.Mutex
	var stored []byte
	putCalls := 0
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		if !strings.HasPrefix(request.URL.Path, "/forja-artifacts/tenants/") || request.Header.Get("Authorization") == "" {
			http.Error(writer, "invalid authority envelope", http.StatusBadRequest)
			return
		}
		switch request.Method {
		case http.MethodPut:
			putCalls++
			if request.Header.Get("If-None-Match") != "*" ||
				request.Header.Get("X-Amz-Checksum-Sha256") != digestBase64 ||
				request.Header.Get("X-Amz-Meta-Forja-Sha256") != digestHex {
				http.Error(writer, "missing conditional integrity headers", http.StatusBadRequest)
				return
			}
			if stored != nil {
				writer.Header().Set("Content-Type", "application/xml")
				writer.WriteHeader(http.StatusPreconditionFailed)
				_, _ = writer.Write([]byte(`<Error><Code>PreconditionFailed</Code><Message>exists</Message></Error>`))
				return
			}
			value, err := io.ReadAll(request.Body)
			if err != nil || !bytes.Equal(value, body) {
				http.Error(writer, "body mismatch", http.StatusBadRequest)
				return
			}
			stored = append([]byte(nil), value...)
			writer.Header().Set("ETag", `"wire-etag"`)
			writer.Header().Set("X-Amz-Version-Id", "wire-version")
			writer.WriteHeader(http.StatusOK)
		case http.MethodGet:
			getCalls++
			if stored == nil {
				http.NotFound(writer, request)
				return
			}
			writer.Header().Set("Content-Type", descriptor.MediaType)
			writer.Header().Set("Content-Length", stringIntForProtocol(int64(len(stored))))
			writer.Header().Set("ETag", `"wire-etag"`)
			writer.Header().Set("X-Amz-Version-Id", "wire-version")
			writer.Header().Set("X-Amz-Checksum-Sha256", digestBase64)
			writer.Header().Set("X-Amz-Meta-Forja-Sha256", digestHex)
			writer.Header().Set("X-Amz-Meta-Forja-Size", stringIntForProtocol(descriptor.SizeBytes))
			writer.Header().Set("X-Amz-Meta-Forja-Media-Type", descriptor.MediaType)
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(stored)
		default:
			http.Error(writer, "unsupported", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "forja-protocol-test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "not-a-production-secret")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	store, err := New(t.Context(), Config{
		Bucket: "forja-artifacts", Region: "us-east-1",
		BaseEndpoint: server.URL, UsePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Publish(t.Context(), testAuthority, descriptor, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Publish(t.Context(), testAuthority, descriptor, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || second.Created || putCalls != 2 || getCalls != 2 ||
		first.ETag != `"wire-etag"` || second.ObjectKey != first.ObjectKey {
		t.Fatalf("first=%#v second=%#v puts=%d gets=%d", first, second, putCalls, getCalls)
	}
}

func stringIntForProtocol(value int64) string {
	if value == 0 {
		return "0"
	}
	var output [20]byte
	position := len(output)
	for value > 0 {
		position--
		output[position] = byte('0' + value%10)
		value /= 10
	}
	return string(output[position:])
}
