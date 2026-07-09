package courses

import (
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Group) {
	e.GET("/courses", HandleGetCourses)
	e.GET("/courses/categories", HandleGetCategories)
	e.GET("/courses/:courseId/topics", HandleGetTopics)
	e.GET("/courses/topics/:topicId/lessons", HandleGetLessons)
}

func HandleGetCourses(ctx echo.Context) error {
	courseList, err := GetAllCourses()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch courses")
	}
	return responses.SendOK(ctx, courseList, "Courses retrieved successfully")
}

func HandleGetCategories(ctx echo.Context) error {
	categories, err := GetAllCategories()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch categories")
	}
	return responses.SendOK(ctx, categories, "Categories retrieved successfully")
}

func HandleGetTopics(ctx echo.Context) error {
	courseID := ctx.Param("courseId")
	topics, err := GetAllTopics(courseID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch topics")
	}
	return responses.SendOK(ctx, topics, "Topics retrieved successfully")
}

func HandleGetLessons(ctx echo.Context) error {
	topicID := ctx.Param("topicId")
	lessons, err := GetAllLessons(topicID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch lessons")
	}
	return responses.SendOK(ctx, lessons, "Lessons retrieved successfully")
}
