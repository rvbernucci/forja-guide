package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

var testAuthority = Authority{
	TenantID:     "00000000-0000-4000-8000-000000000001",
	RepositoryID: "00000000-0000-4000-8000-000000000002",
}

func TestPublishUsesConditionalCreateAndVerifiesCompleteBody(t *testing.T) {
	body := []byte("immutable evidence")
	descriptor := descriptorFor(body, "text/plain")
	fake := &fakeS3{body: body, descriptor: descriptor, etag: `"etag-1"`, version: "v1"}
	store, err := newWithClient("forja-artifacts", fake)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := store.Publish(t.Context(), testAuthority, descriptor, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Created || fake.put == nil || aws.ToString(fake.put.IfNoneMatch) != "*" {
		t.Fatal("publication did not use one conditional create")
	}
	wantChecksum := base64.StdEncoding.EncodeToString(descriptor.SHA256[:])
	if aws.ToString(fake.put.ChecksumSHA256) != wantChecksum || fake.get == nil || evidence.ETag != `"etag-1"` {
		t.Fatal("publication did not preserve checksum and verification evidence")
	}
	wantKey, err := validateAndKey(testAuthority, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ObjectKey != wantKey || aws.ToString(fake.put.Key) != wantKey || aws.ToString(fake.get.Key) != wantKey {
		t.Fatal("object key was not derived consistently")
	}
}

func TestPublishExistingObjectIsIdempotentOnlyAfterVerification(t *testing.T) {
	body := []byte("existing evidence")
	descriptor := descriptorFor(body, "application/octet-stream")
	fake := &fakeS3{
		body: body, descriptor: descriptor, etag: `"etag-existing"`,
		putErr: &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "exists"},
	}
	store, _ := newWithClient("forja-artifacts", fake)
	evidence, err := store.Publish(t.Context(), testAuthority, descriptor, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Created || fake.get == nil {
		t.Fatal("existing object bypassed exact verification")
	}
}

func TestPublishRejectsSourceAndStoredCorruption(t *testing.T) {
	body := []byte("expected")
	descriptor := descriptorFor(body, "text/plain")
	fake := &fakeS3{body: body, descriptor: descriptor}
	store, _ := newWithClient("forja-artifacts", fake)
	if _, err := store.Publish(
		t.Context(), testAuthority, descriptor, bytes.NewReader([]byte("tampered")),
	); !errors.Is(err, ErrIntegrity) || fake.put != nil {
		t.Fatal("source corruption reached the provider")
	}

	fake.body = []byte("corruptd")
	if _, err := store.Verify(t.Context(), testAuthority, descriptor); !errors.Is(err, ErrIntegrity) {
		t.Fatal("stored corruption passed full-body verification")
	}
}

func TestDeleteRequiresCanonicalETag(t *testing.T) {
	body := []byte("purge me")
	descriptor := descriptorFor(body, "text/plain")
	fake := &fakeS3{body: body, descriptor: descriptor}
	store, _ := newWithClient("forja-artifacts", fake)
	if err := store.Delete(t.Context(), testAuthority, descriptor, "", ""); err == nil {
		t.Fatal("purge accepted no transport evidence")
	}
	if err := store.Delete(t.Context(), testAuthority, descriptor, `"etag"`, "version-1"); err != nil {
		t.Fatal(err)
	}
	if fake.deleted == nil || aws.ToString(fake.deleted.IfMatch) != `"etag"` ||
		aws.ToString(fake.deleted.VersionId) != "version-1" {
		t.Fatal("purge was not conditional")
	}
}

func TestReadVerifiedReturnsOnlyBoundedIntegrityCheckedBytes(t *testing.T) {
	body := []byte("approved memory body")
	descriptor := descriptorFor(body, "text/plain")
	fake := &fakeS3{body: body, descriptor: descriptor, etag: "etag", version: "version"}
	store, err := newWithClient("forja-artifacts", fake)
	if err != nil {
		t.Fatal(err)
	}
	got, evidence, err := store.ReadVerified(t.Context(), testAuthority, descriptor, int64(len(body)))
	if err != nil || !bytes.Equal(got, body) || evidence.ETag != "etag" {
		t.Fatalf("body=%q evidence=%#v err=%v", got, evidence, err)
	}
	if _, _, err := store.ReadVerified(t.Context(), testAuthority, descriptor, int64(len(body)-1)); err == nil {
		t.Fatal("undersized retrieval limit succeeded")
	}
	fake.descriptor = descriptorFor(body, "text/markdown")
	if _, _, err := store.ReadVerified(t.Context(), testAuthority, descriptor, int64(len(body))); err == nil {
		t.Fatal("metadata mismatch succeeded")
	}
	fake.descriptor = descriptor
	fake.etag = ""
	if _, _, err := store.ReadVerified(t.Context(), testAuthority, descriptor, int64(len(body))); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing ETag error=%v", err)
	}
}

type fakeS3 struct {
	body       []byte
	descriptor Descriptor
	etag       string
	version    string
	putErr     error
	put        *s3.PutObjectInput
	get        *s3.GetObjectInput
	deleted    *s3.DeleteObjectInput
}

func (f *fakeS3) PutObject(
	_ context.Context,
	input *s3.PutObjectInput,
	_ ...func(*s3.Options),
) (*s3.PutObjectOutput, error) {
	f.put = input
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &s3.PutObjectOutput{ETag: aws.String(f.etag), VersionId: aws.String(f.version)}, nil
}

func (f *fakeS3) GetObject(
	_ context.Context,
	input *s3.GetObjectInput,
	_ ...func(*s3.Options),
) (*s3.GetObjectOutput, error) {
	f.get = input
	checksum := base64.StdEncoding.EncodeToString(f.descriptor.SHA256[:])
	return &s3.GetObjectOutput{
		Body:           io.NopCloser(bytes.NewReader(f.body)),
		ContentLength:  aws.Int64(int64(len(f.body))),
		ContentType:    aws.String(f.descriptor.MediaType),
		ChecksumSHA256: aws.String(checksum),
		ETag:           aws.String(f.etag),
		VersionId:      aws.String(f.version),
		Metadata: map[string]string{
			"forja-sha256":     hex.EncodeToString(f.descriptor.SHA256[:]),
			"forja-size":       strconv.FormatInt(f.descriptor.SizeBytes, 10),
			"forja-media-type": f.descriptor.MediaType,
		},
	}, nil
}

func (f *fakeS3) DeleteObject(
	_ context.Context,
	input *s3.DeleteObjectInput,
	_ ...func(*s3.Options),
) (*s3.DeleteObjectOutput, error) {
	f.deleted = input
	return &s3.DeleteObjectOutput{}, nil
}

func descriptorFor(body []byte, mediaType string) Descriptor {
	return Descriptor{SHA256: sha256.Sum256(body), SizeBytes: int64(len(body)), MediaType: mediaType}
}
