package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexhokl/file-server/db"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	testDBOnce sync.Once
	testDB     *gorm.DB
	testDBSkip string
)

// uniqueUsername returns a username unique to this test run, so tests can run
// concurrently and repeatedly against the same database.
var uniqueCounter atomic.Uint64

func uniqueUsername(prefix string) string {
	return fmt.Sprintf(
		"%s_%d_%d",
		prefix,
		time.Now().UnixNano(),
		uniqueCounter.Add(1),
	)
}

// getTestDatabase returns a connection to the test database described by
// FILESERVER_TEST_DATABASE_CONNECTION_STRING. It skips the test when the
// variable is unset, and refuses to run against any database whose name does
// not contain "test", so the destructive schema reset cannot hit a real
// database.
func getTestDatabase(t *testing.T) *gorm.DB {
	t.Helper()

	testDBOnce.Do(func() {
		gin.SetMode(gin.TestMode)

		connectionString := os.Getenv("FILESERVER_TEST_DATABASE_CONNECTION_STRING")
		if connectionString == "" {
			testDBSkip = "FILESERVER_TEST_DATABASE_CONNECTION_STRING is not set"
			return
		}

		if !strings.Contains(connectionString, "test") {
			testDBSkip = "test database connection string must reference a database with \"test\" in its name"
			return
		}

		conn, err := gorm.Open(postgres.Open(connectionString), &gorm.Config{
			TranslateError: true,
			Logger:         logger.Discard,
		})
		if err != nil {
			testDBSkip = fmt.Sprintf("unable to connect to test database: %v", err)
			return
		}

		// Fresh schema per run; user_credentials first because it references users.
		if err := conn.Migrator().DropTable("user_credentials"); err != nil {
			testDBSkip = fmt.Sprintf("unable to drop user_credentials: %v", err)
			return
		}
		if err := conn.Migrator().DropTable("users"); err != nil {
			testDBSkip = fmt.Sprintf("unable to drop users: %v", err)
			return
		}
		if err := db.Migrate(conn); err != nil {
			testDBSkip = fmt.Sprintf("unable to migrate test database: %v", err)
			return
		}

		testDB = conn
	})

	if testDBSkip != "" {
		t.Skip(testDBSkip)
	}
	return testDB
}

// serve performs a request against the real router with the test database,
// exercising the same middleware wiring as production.
func serve(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	router := GetRouter(getTestDatabase(t))

	recorder := httptest.NewRecorder()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestCreateUser_DuplicateReturns409(t *testing.T) {
	username := uniqueUsername("dup")

	first := serve(t, http.MethodPost, "/users", fmt.Sprintf(`{"username":%q}`, username))
	require.Equal(t, http.StatusCreated, first.Code)

	second := serve(t, http.MethodPost, "/users", fmt.Sprintf(`{"username":%q}`, username))
	assert.Equal(t, http.StatusConflict, second.Code, "duplicate username must be 409, not 500")
}

func TestCreateUser_EmptyUsernameReturns400(t *testing.T) {
	response := serve(t, http.MethodPost, "/users", `{"username":""}`)
	assert.Equal(t, http.StatusBadRequest, response.Code)
}

func TestDeleteUser_MissingReturns404(t *testing.T) {
	username := uniqueUsername("missing")

	response := serve(t, http.MethodDelete, "/users/"+username, "")
	assert.Equal(t, http.StatusNotFound, response.Code, "deleting a missing user must be 404, not 204")
}

func TestDeleteUser_WithCredentialsReturns409(t *testing.T) {
	username := uniqueUsername("fkuser")

	require.Equal(t, http.StatusCreated, serve(t, http.MethodPost, "/users", fmt.Sprintf(`{"username":%q}`, username)).Code)

	credential := serve(t, http.MethodPost, "/users/"+username+"/credentials",
		`{"public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC0KaA6moJ3o9DhQAeDre8bfu1x9VXv547ewbOknMKZi test"}`)
	require.Equal(t, http.StatusCreated, credential.Code)

	response := serve(t, http.MethodDelete, "/users/"+username, "")
	assert.Equal(t, http.StatusConflict, response.Code,
		"deleting a user with credentials must be 409 while the foreign key restricts it")
}

func TestDeleteUser_AfterCredentialRemovalReturns204(t *testing.T) {
	username := uniqueUsername("lifecycle")

	require.Equal(t, http.StatusCreated, serve(t, http.MethodPost, "/users", fmt.Sprintf(`{"username":%q}`, username)).Code)

	credential := serve(t, http.MethodPost, "/users/"+username+"/credentials",
		`{"public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGisgOaBXtfGcR5nUmlT41WdCyF49ni7HAOLPLtc7FI8 test2"}`)
	require.Equal(t, http.StatusCreated, credential.Code)

	var credentials []struct {
		Id uint `json:"id"`
	}
	listResponse := serve(t, http.MethodGet, "/users/"+username+"/credentials", "")
	require.Equal(t, http.StatusOK, listResponse.Code)
	require.NoError(t, json.Unmarshal(listResponse.Body.Bytes(), &credentials))
	require.Len(t, credentials, 1)

	deleteCredential := serve(t, http.MethodDelete,
		fmt.Sprintf("/users/%s/credentials/%d", username, credentials[0].Id), "")
	require.Equal(t, http.StatusNoContent, deleteCredential.Code)

	deleteUser := serve(t, http.MethodDelete, "/users/"+username, "")
	assert.Equal(t, http.StatusNoContent, deleteUser.Code,
		"once credentials are removed the user must be deletable")
}

func TestDeleteUserCredential_MissingReturns404(t *testing.T) {
	response := serve(t, http.MethodDelete, "/users/whatever/credentials/999999", "")
	assert.Equal(t, http.StatusNotFound, response.Code, "deleting a missing credential must be 404, not 204")
}

func TestCreateUserCredential_MissingUserReturns404(t *testing.T) {
	username := uniqueUsername("ghost")

	response := serve(t, http.MethodPost, "/users/"+username+"/credentials",
		`{"public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKlOqD39HHKMpG/Q9v2KJvUsu/4NhBBSPEaU2GnBZofq test3"}`)
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestCreateUserCredential_DuplicateKeyReturns409(t *testing.T) {
	username := uniqueUsername("dupkey")
	key := `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIlnXvjeJDFMMzWq4CbqVtYEcoNvm8R0amr7iRyQFKPb dup`

	require.Equal(t, http.StatusCreated, serve(t, http.MethodPost, "/users", fmt.Sprintf(`{"username":%q}`, username)).Code)

	first := serve(t, http.MethodPost, "/users/"+username+"/credentials", fmt.Sprintf(`{"public_key":%q}`, key))
	require.Equal(t, http.StatusCreated, first.Code)

	second := serve(t, http.MethodPost, "/users/"+username+"/credentials", fmt.Sprintf(`{"public_key":%q}`, key))
	assert.Equal(t, http.StatusConflict, second.Code, "duplicate public key must be 409, not 500")
}

func TestCreateUserCredential_InvalidKeyReturns400(t *testing.T) {
	username := uniqueUsername("badkey")

	require.Equal(t, http.StatusCreated, serve(t, http.MethodPost, "/users", fmt.Sprintf(`{"username":%q}`, username)).Code)

	response := serve(t, http.MethodPost, "/users/"+username+"/credentials", `{"public_key":"not a key"}`)
	assert.Equal(t, http.StatusBadRequest, response.Code)
}

func TestListUsers(t *testing.T) {
	username := uniqueUsername("list")

	require.Equal(t, http.StatusCreated, serve(t, http.MethodPost, "/users", fmt.Sprintf(`{"username":%q}`, username)).Code)

	response := serve(t, http.MethodGet, "/users", "")
	require.Equal(t, http.StatusOK, response.Code)

	var users []string
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &users))
	assert.Contains(t, users, username)
}

func TestListUserCredentials_MissingUserReturns404(t *testing.T) {
	username := uniqueUsername("ghost")

	response := serve(t, http.MethodGet, "/users/"+username+"/credentials", "")
	assert.Equal(t, http.StatusNotFound, response.Code, "listing credentials of a missing user must be 404, not 200 []")
}