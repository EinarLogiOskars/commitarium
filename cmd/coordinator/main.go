package main

import (
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/httpapi"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

func main() {

	store := project.NewMemoryStore()
	projectService := project.NewService(store)
	handler := httpapi.New(projectService)

	log.Print("Listening...")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		log.Fatal(err)
	}

}
