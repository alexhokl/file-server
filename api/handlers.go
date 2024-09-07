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
		c.AbortWithError(
			http.StatusBadRequest,
			fmt.Errorf("unable to parse public key"),
		)
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
