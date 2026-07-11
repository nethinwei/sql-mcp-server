// Package configyaml decodes YAML runtime configuration.
package configyaml

import (
	"os"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"gopkg.in/yaml.v3"
)

// Decode decodes, defaults, and validates a YAML configuration.
func Decode(data []byte) (*config.Config, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}

	var cfg config.Config
	root := documentRoot(&document)
	if root != nil {
		if err := root.Decode(&cfg); err != nil {
			return nil, err
		}
		cfg.ApplyPresence(collectPresence(root, len(cfg.Entities)))
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Load reads and decodes a YAML configuration file.
func Load(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

func documentRoot(document *yaml.Node) *yaml.Node {
	if document.Kind == yaml.DocumentNode && len(document.Content) > 0 {
		return document.Content[0]
	}
	if document.Kind != 0 {
		return document
	}
	return nil
}

func collectPresence(root *yaml.Node, entityCount int) config.Presence {
	presence := config.Presence{
		EntityDMLTools: make([]bool, entityCount),
	}

	_, presence.Tools = mappingValue(root, "tools")
	if costNode, ok := mappingValue(root, "cost"); ok {
		presence.Cost = mappingFields(costNode)
		if aqeNode, ok := mappingValue(costNode, "aqe"); ok {
			presence.CostAQE = mappingFields(aqeNode)
		}
	}

	if entitiesNode, ok := mappingValue(root, "entities"); ok && entitiesNode.Kind == yaml.SequenceNode {
		for i, entityNode := range entitiesNode.Content {
			if i >= len(presence.EntityDMLTools) {
				break
			}
			mcpNode, ok := mappingValue(entityNode, "mcp")
			if !ok {
				continue
			}
			_, presence.EntityDMLTools[i] = mappingValue(mcpNode, "dmlTools")
		}
	}
	return presence
}

func mappingFields(node *yaml.Node) map[string]bool {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	fields := make(map[string]bool, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		fields[node.Content[i].Value] = true
	}
	return fields
}

func mappingValue(node *yaml.Node, key string) (*yaml.Node, bool) {
	if node.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], true
		}
	}
	return nil, false
}
