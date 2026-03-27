package api

import "github.com/gin-gonic/gin"

func errorJSON(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": message, "code": code})
}
