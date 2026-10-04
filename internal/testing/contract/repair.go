package contract

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// This file holds every divergence between the committed trimmed copy and
// Microsoft's original description. Each rule exists because the original would
// reject a request the api-reference says is correct, and each rule is covered
// by a test in repair_test.go. Prefer few, explicit, tested rules over a generic
// "sanitise everything" pass.

// odataTypeRequiredRule rewrites Microsoft's blanket "@odata.type is required".
//
// refs/openapi/openapi/v1.0/openapi.yaml marks "@odata.type" as required in
// thousands of schemas – microsoft.graph.entity requires it, and every
// microsoft.graph.chatMessage is an allOf of microsoft.graph.entity plus a
// second object that requires it again – while the api-reference says only the
// body property is mandatory on a message write:
//
//	refs/graph/api-reference/v1.0/api/channel-post-messages.md, "Request body":
//	"In the request body, supply a JSON representation of a chatMessage object.
//	 Only the body property is mandatory. All other properties are optional."
//
// The code we port omits @odata.type (it is a client-side type discriminator,
// not a field the Teams UI sends on create), so without this rule the very first
// correct POST would fail the contract test. See PLAN.md Layer 6, "Expect and
// resolve the @odata.type conflict".
//
// The rule removes "@odata.type" from every "required" array in every retained
// schema. The property itself stays declared, so clients that do send it are
// still validated.
//
// Returns the number of required arrays that changed.
func stripODataTypeRequired(pathNodes map[string]*yaml.Node, keep map[string]map[string]bool, index map[string]map[string]*yaml.Node) (int, error) {
	changed := 0
	apply := func(n *yaml.Node) {
		changed += stripFromRequiredArrays(n)
	}
	for _, name := range sortedKeys(pathNodes) {
		apply(pathNodes[name])
	}
	for _, name := range sortedKeys2(keep["schemas"]) {
		node := index["schemas"][name]
		if node == nil {
			return changed, fmt.Errorf("contract: schema %q vanished during the @odata.type pass", name)
		}
		apply(node)
	}
	// Request bodies and responses can carry inline schemas too.
	for _, cat := range []string{"requestBodies", "responses"} {
		for _, name := range sortedKeys2(keep[cat]) {
			if node := index[cat][name]; node != nil {
				apply(node)
			}
		}
	}
	return changed, nil
}

// stripFromRequiredArrays walks a subtree and removes the "@odata.type" entry
// from every "required" sequence it finds.
func stripFromRequiredArrays(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	changed := 0
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if key.Value == "required" && val.Kind == yaml.SequenceNode {
				kept := val.Content[:0]
				for _, item := range val.Content {
					if item.Kind == yaml.ScalarNode && item.Value == odataTypeKey {
						changed++
						continue
					}
					kept = append(kept, item)
				}
				val.Content = kept
				if len(val.Content) == 0 {
					// An empty required list is invalid OpenAPI ("minItems: 1").
					// Drop the key entirely.
					n.Content = append(n.Content[:i], n.Content[i+2:]...)
					i -= 2
					continue
				}
			}
			changed += stripFromRequiredArrays(val)
		}
		return changed
	}
	for _, c := range n.Content {
		changed += stripFromRequiredArrays(c)
	}
	return changed
}

// discriminatorMappingRule prunes discriminator mappings that point at schemas
// the trim dropped.
//
// Microsoft's microsoft.graph.entity carries a discriminator mapping with one
// entry per entity type in the whole description (thousands of entries). Every
// schema that derives from entity inherits it, so keeping the mapping as-is
// would pull the entire description back in through the $ref closure and blow
// the size budget.
//
// Pruning is safe for validation: kin-openapi only consults a discriminator
// mapping when the input actually carries the discriminator property, and the
// entries that survive are exactly the ones whose target schema was kept.
// Entries pointing at dropped schemas are removed, and a discriminator with no
// remaining entries is dropped altogether.
//
// Returns the number of removed mapping entries.
func pruneDiscriminatorMappings(pathNodes map[string]*yaml.Node, keep map[string]map[string]bool, index map[string]map[string]*yaml.Node) (int, error) {
	removed := 0
	prune := func(n *yaml.Node) {
		removed += pruneDiscriminators(n, keep)
	}
	for _, name := range sortedKeys(pathNodes) {
		prune(pathNodes[name])
	}
	for _, name := range sortedKeys2(keep["schemas"]) {
		prune(index["schemas"][name])
	}
	return removed, nil
}

func pruneDiscriminators(n *yaml.Node, keep map[string]map[string]bool) int {
	if n == nil {
		return 0
	}
	removed := 0
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if key.Value == "discriminator" {
				removed += pruneDiscriminatorMapping(val, keep)
			}
			removed += pruneDiscriminators(val, keep)
		}
	}
	if n.Kind == yaml.SequenceNode {
		for _, c := range n.Content {
			removed += pruneDiscriminators(c, keep)
		}
	}
	return removed
}

func pruneDiscriminatorMapping(disc *yaml.Node, keep map[string]map[string]bool) int {
	if disc == nil || disc.Kind != yaml.MappingNode {
		return 0
	}
	mapping := mappingValue(disc, "mapping")
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return 0
	}
	removed := 0
	kept := make([]*yaml.Node, 0, len(mapping.Content))
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		ref := mapping.Content[i+1].Value
		cat, name, ok := splitComponentRef(ref)
		if !ok {
			// A mapping entry that is not a component ref: keep it, the
			// post-condition check in trimDocument will complain if it dangles.
			kept = append(kept, mapping.Content[i], mapping.Content[i+1])
			continue
		}
		if keep[cat][name] {
			kept = append(kept, mapping.Content[i], mapping.Content[i+1])
			continue
		}
		removed++
	}
	mapping.Content = kept
	if removed > 0 && len(mapping.Content) == 0 {
		// No usable mapping left; drop the whole discriminator object.
		for i := 0; i+1 < len(disc.Content); i += 2 {
			if disc.Content[i].Value == "mapping" {
				disc.Content = append(disc.Content[:i], disc.Content[i+2:]...)
				break
			}
		}
	}
	return removed
}
