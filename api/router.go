package api

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"github.com/alexhokl/file-server/docs"
	swaggerfiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

func GetRouter(dialector gorm.Dialector) (*gin.Engine, error) {
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// Open API documentation
	docs.SwaggerInfo.BasePath = "/"
	docs.SwaggerInfo.Version = "1.0"
	docs.SwaggerInfo.Title = "File Server API"
	r.GET("/docs/*any", ginSwagger.WrapHandler(swaggerfiles.Handler))

	// User APIs
	users := r.Group("/users", requiredAdminAccess(), withDatabaseConnection(dialector))
	users.GET("", ListUsers)
	users.POST("", CreateUser)
	users.DELETE("/:username", DeleteUser)

	// User credential APIs
	userCredentials := users.Group("/:username/credentials")
	userCredentials.GET("", ListUserCredentials)
	userCredentials.POST("", CreateUserCredential)
	userCredentials.DELETE("/:credential_id", DeleteUserCredential)

	return r, nil
}
