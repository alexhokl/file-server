package api

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetRouter(dialector gorm.Dialector) (*gin.Engine, error) {
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	users := r.Group("/users", requiredAdminAccess(), withDatabaseConnection(dialector))
	users.GET("", ListUsers)
	users.POST("", CreateUser)
	users.DELETE("/:username", DeleteUser)

	userCredentials := users.Group("/:username/credentials")
	userCredentials.GET("", ListUserCredentials)
	userCredentials.POST("", CreateUserCredential)
	userCredentials.DELETE("/:credential_id", DeleteUserCredential)

	return r, nil
}
