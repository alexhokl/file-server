package api

import (
	"log/slog"
	"net/http"

	"github.com/alexhokl/helper/database"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func withDatabaseConnection(dialector gorm.Dialector) gin.HandlerFunc {
	return func(c *gin.Context) {
		dbConn, err := database.GetDatabaseConnection(dialector)
		if err != nil {
			slog.Error(
				"unable to get database connection",
				slog.String("error", err.Error()),
			)
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Set("db", dbConn)
		c.Next()
	}
}

func getDatabaseConnectionFromContext(c *gin.Context) (*gorm.DB, bool) {
	dbConnObj, ok := c.Get("db")
	if !ok {
		return nil, false
	}

	dbConn, ok := dbConnObj.(*gorm.DB)
	if !ok {
		return nil, false
	}

	return dbConn, true
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
