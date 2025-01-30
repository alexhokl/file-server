package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexhokl/file-server/db"
	"github.com/gin-gonic/gin"
	"github.com/gliderlabs/ssh"
	"gorm.io/gorm"
)

// ListUsers godoc
//
//	@Summary		List users
//	@Description	List all users
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Success		200	{array}	string	"list of user names"
//	@Router			/users [get]
func ListUsers(c *gin.Context) {
	dbConn, ok := getDatabaseConnectionFromContext(c)
	if !ok {
		slog.Error("unable to retrieve database connection")
		c.Status(http.StatusInternalServerError)
		return
	}

	var users []db.User
	dbConn.Find(&users)

	list := make([]string, len(users))
	for i, user := range users {
		list[i] = user.Username
	}

	c.JSON(http.StatusOK, users)
}

// CreateUser godoc
//
//	@Summary		Create user
//	@Description	Create a new user
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Param			request	body		createUserRequest	true	"User information"
//	@Success		201		{object}	createdUserResponse
//	@Failure		400		"empty username"
//	@Failure		409		"username already exists"
//	@Failure		500		"unable to create user"
//	@Router			/users [post]
func CreateUser(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

	dbConn, ok := getDatabaseConnectionFromContext(c)
	if !ok {
		slog.Error("unable to retrieve database connection")
		c.Status(http.StatusInternalServerError)
		return
	}

	user := db.User{
		Username: req.Username,
	}

	if err := dbConn.Create(&user).Error; err != nil {
		if err == gorm.ErrDuplicatedKey {
			c.Status(http.StatusConflict)
			return
		}

		slog.Error(
			"unable to create user",
			slog.String("error", err.Error()),
			slog.String("username", user.Username),
		)
		c.Status(http.StatusInternalServerError)
		return
	}

	viewModel := createdUserResponse{
		Username: user.Username,
	}

	c.JSON(http.StatusCreated, viewModel)
}

// DeleteUser godoc
//
//	@Summary		Delete user
//	@Description	Delete a user
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Param			username	path	string	true	"Username"
//	@Success		204			"user deleted"
//	@Failure		400			"empty username"
//	@Failure		404			"user not found"
//	@Failure		500			"unable to delete user"
//	@Router			/users/{username} [delete]
func DeleteUser(c *gin.Context) {
	username := c.Param("username")
	if username == "" {
		c.Status(http.StatusBadRequest)
		return
	}

	dbConn, ok := getDatabaseConnectionFromContext(c)
	if !ok {
		slog.Error("unable to retrieve database connection")
		c.Status(http.StatusInternalServerError)
		return
	}

	if err := dbConn.Where("username = ?", username).Delete(&db.User{}).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Status(http.StatusNotFound)
			return
		}

		slog.Error(
			"unable to delete user",
			slog.String("error", err.Error()),
			slog.String("username", username),
		)
		c.Status(http.StatusInternalServerError)
		return
	}

	c.Status(http.StatusNoContent)
}

// ListUserCredentials godoc
//
//	@Summary		List user credentials
//	@Description	List all credentials of a user
//	@Tags			credentials
//	@Accept			json
//	@Produce		json
//	@Param			username	path	string	true	"Username"
//	@Success		200			{array}	credentialInfo
//	@Failure		400			"empty username"
//	@Failure		404			"user not found"
//	@Failure		500			"unable to retrieve user credentials"
//	@Router			/users/{username}/credentials [get]
func ListUserCredentials(c *gin.Context) {
	username := c.Param("username")
	if username == "" {
		c.Status(http.StatusBadRequest)
		return
	}

	dbConn, ok := getDatabaseConnectionFromContext(c)
	if !ok {
		slog.Error("unable to retrieve database connection")
		c.Status(http.StatusInternalServerError)
		return
	}

	var credentials []db.UserCredential
	dbConn.Where("username = ?", username).Find(&credentials)

	keys := make([]credentialInfo, len(credentials))
	for i, credential := range credentials {
		keys[i] = credentialInfo{
			Id:        credential.ID,
			PublicKey: credential.PublicKey,
		}
	}

	c.JSON(http.StatusOK, keys)
}

// CreateUserCredential godoc
//
//	@Summary		Create user credential
//	@Description	Create a new credential for a user
//	@Tags			credentials
//	@Accept			json
//	@Produce		json
//	@Param			username	path		string						true	"Username"
//	@Param			request		body		createUserCredentialRequest	true	"Credential information"
//	@Success		201			{object}	createUserCredentialResponse
//	@Failure		400			"empty username or invalid public key"
//	@Failure		404			"user not found"
//	@Failure		409			"public key already exists"
//	@Failure		500			"unable to create user credential"
//	@Router			/users/{username}/credentials [post]
func CreateUserCredential(c *gin.Context) {
	username := c.Param("username")
	if username == "" {
		c.Status(http.StatusBadRequest)
		return
	}

	var req createUserCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		slog.Warn("bad request")
		c.Status(http.StatusBadRequest)
		return
	}

	_, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.PublicKey))
	if err != nil {
		slog.Warn(
			"unable to parse public key",
			slog.String("error", err.Error()),
			slog.String("username", username),
			slog.String("public_key", req.PublicKey),
		)
		ginErr := c.AbortWithError(
			http.StatusBadRequest,
			fmt.Errorf("unable to parse public key"),
		)
		if ginErr != nil {
			slog.Error(
				"unable to serve error response (unable to parse public key)",
				slog.String("error", err.Error()),
				slog.String("gin_error", ginErr.Error()),
			)
			c.Status(http.StatusInternalServerError)
		}
		return
	}

	dbConn, ok := getDatabaseConnectionFromContext(c)
	if !ok {
		slog.Error("unable to retrieve database connection")
		c.Status(http.StatusInternalServerError)
		return
	}

	if err := dbConn.Where("username = ?", username).First(&db.User{}).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Status(http.StatusNotFound)
			return
		}

		slog.Error(
			"unable to retrieve user",
			slog.String("error", err.Error()),
			slog.String("username", username),
		)
		c.Status(http.StatusInternalServerError)
		return
	}

	credential := db.UserCredential{
		Username:  username,
		PublicKey: req.PublicKey,
	}

	if err := dbConn.Create(&credential).Error; err != nil {
		if err == gorm.ErrDuplicatedKey {
			c.Status(http.StatusConflict)
			return
		}

		slog.Error(
			"unable to create user credential",
			slog.String("error", err.Error()),
			slog.String("username", credential.Username),
			slog.String("public_key", credential.PublicKey),
		)
		c.Status(http.StatusInternalServerError)
		return
	}

	viewModel := createUserCredentialResponse{
		ID:        credential.ID,
		Username:  credential.Username,
		PublicKey: credential.PublicKey,
		CreatedAt: credential.CreatedAt.Format(time.RFC3339),
	}

	c.JSON(http.StatusCreated, viewModel)
}

// DeleteUserCredential godoc
//
//	@Summary		Delete user credential
//	@Description	Delete a credential of a user
//	@Tags			credentials
//	@Accept			json
//	@Produce		json
//	@Param			username		path	string	true	"Username"
//	@Param			credential_id	path	string	true	"Credential ID"
//	@Success		204				"credential deleted"
//	@Failure		400				"empty username or credential ID"
//	@Failure		404				"credential not found"
//	@Failure		500				"unable to delete user credential"
//	@Router			/users/{username}/credentials/{credential_id} [delete]
func DeleteUserCredential(c *gin.Context) {
	credentialID := c.Param("credential_id")
	if credentialID == "" {
		c.Status(http.StatusBadRequest)
		return
	}

	dbConn, ok := getDatabaseConnectionFromContext(c)
	if !ok {
		slog.Error("unable to retrieve database connection")
		c.Status(http.StatusInternalServerError)
		return
	}

	if err := dbConn.Where("id = ?", credentialID).Delete(&db.UserCredential{}).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Status(http.StatusNotFound)
			return
		}

		slog.Error(
			"unable to delete user credential",
			slog.String("error", err.Error()),
			slog.String("credential_id", credentialID),
		)
		c.Status(http.StatusInternalServerError)
		return
	}
}
