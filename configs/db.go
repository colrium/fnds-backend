package configs

import (
	"context"
	"fmt"
	"log"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type MongoInstance struct {
	Client *mongo.Client
	DB     *mongo.Database
}

var MI MongoInstance

func ConnectDB() {
	dbName := fmt.Sprintf("%s-fundis", AppEnv())
	serverAPI := options.ServerAPI(options.ServerAPIVersion1)
	opts := options.Client().ApplyURI(EnvMongoURI()).SetServerAPIOptions(serverAPI)
	client, err := mongo.Connect(context.TODO(), opts)
	if err != nil {
		log.Fatalf("failed to connect to MongoDB: %v", err)
	}
	// defer func() {
	// 	if err = client.Disconnect(context.TODO()); err != nil {
	// 		panic(err)
	// 	}
	// }()
	var result bson.M
	if err := client.Database(dbName).RunCommand(context.TODO(), bson.D{{Key: "ping", Value: 1}}).Decode(&result); err != nil {
		log.Fatalf("failed to ping MongoDB: %v", err)
	}
	fmt.Println("Pinged your deployment. You successfully connected to MongoDB!")

	MI = MongoInstance{
		Client: client,
		DB:     client.Database(dbName),
	}
}

func StoreRequestInDb(request any, collection string) (string, error) {
	coll := MI.DB.Collection(collection)
	insertResult, err := coll.InsertOne(context.TODO(), request)
	if err != nil {
		log.Printf("there was an error recording the request \n%v\n", err)
		return "", err
	}
	uuid, ok := insertResult.InsertedID.(primitive.ObjectID)
	if !ok {
		return "", err
	}
	return uuid.Hex(), nil
}

// TransactionsSupported reports whether the connected deployment can run
// multi-document transactions, determined once at startup from the hello
// command: a replica set reports setName, mongos reports msg == "isdbgrid".
func TransactionsSupported(ctx context.Context) (bool, error) {
	var result bson.M
	if err := MI.Client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&result); err != nil {
		return false, fmt.Errorf("running hello command: %w", err)
	}

	// A replica set member reports a non-empty "setName" field.
	if setName, ok := result["setName"]; ok {
		if s, isStr := setName.(string); isStr && s != "" {
			return true, nil
		}
	}

	// A mongos instance reports msg == "isdbgrid".
	if msg, ok := result["msg"]; ok {
		if s, isStr := msg.(string); isStr && s == "isdbgrid" {
			return true, nil
		}
	}

	// Standalone mongod — transactions are not supported.
	return false, nil
}

func UpdateRequestInDb(ID string, request any, collection string) (string, error) {
	coll := MI.DB.Collection(collection)
	filter := bson.M{"_id": ID}
	update := bson.M{"$set": request}
	_, err := coll.UpdateOne(context.TODO(), filter, update)
	if err != nil {
		log.Printf("there was an error updating the request \n%v\n", err)
		return "", err
	}
	return ID, nil
}
