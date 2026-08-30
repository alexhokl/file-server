package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func withDatabaseConnection(dbConn *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("db", dbConn)
		c.Next()
	}
}

func getDatabaseConnectionFromContext(c *gin.Context) *gorm.DB {
	return c.MustGet("db").(*gorm.DB)
}

func requiredAdminAccess() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isAdmin(c) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
}

func isAdmin(c *gin.Context) bool {
	//TODO: implement
	return true
}
