package docs

import (
	_ "embed"

	"github.com/swaggo/swag"
)

const ServingInstance = "engram-served"

//go:embed swagger.json
var servedJSON string

type servedDocument struct{}

func (servedDocument) ReadDoc() string { return servedJSON }

func init() {
	swag.Register(ServingInstance, servedDocument{})
}
