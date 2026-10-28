package googleworkspace

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	testGmailAdminEmail      = "admin@example.com"
	testGmailMailboxOwner    = "mailbox-owner@example.com"
	testGmailServiceAccount  = "terraform@my-project.iam.gserviceaccount.com"
	testGmailFakeAccessToken = "fake-access-token"
	testGmailCustomer        = "my_customer"
	testGmailUserAgent       = "terraform-provider-googleworkspace/test"
	testGmailSettingsScope   = "https://www.googleapis.com/auth/gmail.settings.basic"
	testGmailDirectoryScope  = "https://www.googleapis.com/auth/admin.directory.user"
)

// disableApplicationDefaultCredentials points ADC at a file that does not
// exist, so a client that falls back to ADC fails instead of silently
// picking up the developer's own gcloud login.
func disableApplicationDefaultCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
}

func TestConfigImpersonatedClient_accessToken(t *testing.T) {
	config := &apiClient{
		client:                &http.Client{},
		AccessToken:           testGmailFakeAccessToken,
		ClientScopes:          []string{testGmailSettingsScope, testGmailDirectoryScope},
		Customer:              testGmailCustomer,
		ImpersonatedUserEmail: testGmailAdminEmail,
		ServiceAccount:        testGmailServiceAccount,
		UserAgent:             testGmailUserAgent,
	}

	got := config.impersonatedClient(testGmailMailboxOwner)

	want := &apiClient{
		AccessToken:           testGmailFakeAccessToken,
		ClientScopes:          []string{testGmailSettingsScope, testGmailDirectoryScope},
		Customer:              testGmailCustomer,
		ImpersonatedUserEmail: testGmailMailboxOwner,
		ServiceAccount:        testGmailServiceAccount,
		UserAgent:             testGmailUserAgent,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("impersonated client mismatch:\n got: %+v\nwant: %+v", got, want)
	}

	if config.ImpersonatedUserEmail != testGmailAdminEmail {
		t.Fatalf("provider client subject changed to %q", config.ImpersonatedUserEmail)
	}
	if config.client == nil {
		t.Fatalf("provider client lost its HTTP client")
	}
}

func TestConfigImpersonatedClient_credentials(t *testing.T) {
	config := &apiClient{
		client:                &http.Client{},
		ClientScopes:          []string{testGmailSettingsScope},
		Credentials:           testFakeCredentialsPath,
		Customer:              testGmailCustomer,
		ImpersonatedUserEmail: testGmailAdminEmail,
		UserAgent:             testGmailUserAgent,
	}

	got := config.impersonatedClient(testGmailMailboxOwner)

	want := &apiClient{
		ClientScopes:          []string{testGmailSettingsScope},
		Credentials:           testFakeCredentialsPath,
		Customer:              testGmailCustomer,
		ImpersonatedUserEmail: testGmailMailboxOwner,
		UserAgent:             testGmailUserAgent,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("impersonated client mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

// TestConfigNewGmailService_accessToken covers keyless domain-wide delegation:
// the provider has an access_token for a principal that may sign as
// service_account, and no key file. The per-user Gmail client must mint its
// token through the service account rather than fall back to ADC.
func TestConfigNewGmailService_accessToken(t *testing.T) {
	disableApplicationDefaultCredentials(t)

	config := &apiClient{
		AccessToken:           testGmailFakeAccessToken,
		ClientScopes:          []string{testGmailSettingsScope},
		Customer:              testGmailCustomer,
		ImpersonatedUserEmail: testGmailAdminEmail,
		ServiceAccount:        testGmailServiceAccount,
	}

	gmailService, diags := config.NewGmailService(context.Background(), testGmailMailboxOwner)
	if err := checkDiags(diags); err != nil {
		t.Fatalf("%s", err.Error())
	}
	if gmailService == nil {
		t.Fatalf("expected a Gmail service, got nil")
	}
}

// A static access_token cannot act as another user, so without
// service_account the per-user client reports that instead of quietly
// authenticating some other way.
func TestConfigNewGmailService_accessTokenWithoutServiceAccount(t *testing.T) {
	disableApplicationDefaultCredentials(t)

	config := &apiClient{
		AccessToken:  testGmailFakeAccessToken,
		ClientScopes: []string{testGmailSettingsScope},
		Customer:     testGmailCustomer,
	}

	_, diags := config.NewGmailService(context.Background(), testGmailMailboxOwner)
	err := checkDiags(diags)
	if err == nil {
		t.Fatalf("expected error, but got nil")
	}
	if !strings.Contains(err.Error(), "service_account") {
		t.Fatalf("expected a service_account error, got: %s", err.Error())
	}
}

func TestConfigNewGmailService_credentials(t *testing.T) {
	disableApplicationDefaultCredentials(t)

	config := &apiClient{
		ClientScopes:          []string{testGmailSettingsScope},
		Credentials:           testFakeCredentialsPath,
		Customer:              testGmailCustomer,
		ImpersonatedUserEmail: testGmailAdminEmail,
	}

	gmailService, diags := config.NewGmailService(context.Background(), testGmailMailboxOwner)
	if err := checkDiags(diags); err != nil {
		t.Fatalf("%s", err.Error())
	}
	if gmailService == nil {
		t.Fatalf("expected a Gmail service, got nil")
	}
}
