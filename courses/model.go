package courses

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Course struct {
	ID          string    `json:"id" bson:"_id"`
	CategoryID  string    `json:"category_id" bson:"category_id"`
	Title       string    `json:"title" bson:"title"`
	Description string    `json:"description" bson:"description"`
	Duration    int       `json:"duration" bson:"duration"`
	Image       string    `json:"image" bson:"image"`
	IsActive    bool      `json:"is_active" bson:"is_active"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

type CourseCategory struct {
	ID          string    `json:"id" bson:"_id"`
	Name        string    `json:"name" bson:"name"`
	Description string    `json:"description" bson:"description"`
	Image       string    `json:"image" bson:"image"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

type CourseTopic struct {
	ID          string    `json:"id" bson:"_id"`
	CourseID    string    `json:"course_id" bson:"course_id"`
	Title       string    `json:"title" bson:"title"`
	Description string    `json:"description" bson:"description"`
	Order       int       `json:"order" bson:"order"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

type Lesson struct {
	ID        string    `json:"id" bson:"_id"`
	TopicID   string    `json:"topic_id" bson:"topic_id"`
	Title     string    `json:"title" bson:"title"`
	Content   string    `json:"content" bson:"content"`
	Duration  int       `json:"duration" bson:"duration"`
	VideoURL  string    `json:"video_url" bson:"video_url"`
	Order     int       `json:"order" bson:"order"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateCourse(c *Course) error {
	c.ID = uuid.New().String()
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("courses").InsertOne(context.TODO(), c)
	return err
}

func GetAllCourses() ([]Course, error) {
	cursor, err := configs.MI.DB.Collection("courses").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var courseList []Course
	if err := cursor.All(context.TODO(), &courseList); err != nil {
		return nil, err
	}
	return courseList, nil
}

func CreateCategory(cat *CourseCategory) error {
	cat.ID = uuid.New().String()
	cat.CreatedAt = time.Now()
	cat.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("course_categories").InsertOne(context.TODO(), cat)
	return err
}

func GetAllCategories() ([]CourseCategory, error) {
	cursor, err := configs.MI.DB.Collection("course_categories").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var categories []CourseCategory
	if err := cursor.All(context.TODO(), &categories); err != nil {
		return nil, err
	}
	return categories, nil
}

func CreateTopic(t *CourseTopic) error {
	t.ID = uuid.New().String()
	t.CreatedAt = time.Now()
	t.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("course_topics").InsertOne(context.TODO(), t)
	return err
}

func GetAllTopics(courseID string) ([]CourseTopic, error) {
	cursor, err := configs.MI.DB.Collection("course_topics").Find(context.TODO(), bson.M{"course_id": courseID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var topics []CourseTopic
	if err := cursor.All(context.TODO(), &topics); err != nil {
		return nil, err
	}
	return topics, nil
}

func CreateLesson(l *Lesson) error {
	l.ID = uuid.New().String()
	l.CreatedAt = time.Now()
	l.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("lessons").InsertOne(context.TODO(), l)
	return err
}

func GetAllLessons(topicID string) ([]Lesson, error) {
	cursor, err := configs.MI.DB.Collection("lessons").Find(context.TODO(), bson.M{"topic_id": topicID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var lessons []Lesson
	if err := cursor.All(context.TODO(), &lessons); err != nil {
		return nil, err
	}
	return lessons, nil
}
