package api

import (
	"github.com/alexhokl/file-server/docs"
	"github.com/gin-gonic/gin"
	swaggerfiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/gorm"
)

func GetRouter(dbConn *gorm.DB) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// Open API documentation
	docs.SwaggerInfo.BasePath = "/"
	docs.SwaggerInfo.Version = "1.0"
	docs.SwaggerInfo.Title = "File Server API"
	r.GET("/docs/*any", ginSwagger.WrapHandler(swaggerfiles.Handler))

	// User APIs
	users := r.Group("/users", requiredAdminAccess(), withDatabaseConnection(dbConn))
	users.GET("", ListUsers)
	users.POST("", CreateUser)
	users.DELETE("/:username", DeleteUser)

	// User credential APIs
	userCredentials := users.Group("/:username/credentials")
	userCredentials.GET("", ListUserCredentials)
	userCredentials.POST("", CreateUserCredential)
	userCredentials.DELETE("/:credential_id", DeleteUserCredential)

	return r
}
