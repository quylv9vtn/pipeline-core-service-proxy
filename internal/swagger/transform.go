package swagger

import (
	"encoding/json"
	"fmt"
)

// regionParam is injected into every operation as a Swagger 2.0 parameter.
const regionParam = `{"name":"region","in":"query","type":"string","required":true,` +
	`"description":"Target region (required by pipeline-core-service). ` +
	`May alternatively be supplied via the X-Region header or a top-level ` +
	`\"region\" field in a JSON request body. Precedence: header > query > body."}`

var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true,
}

// InjectRegionParam transforms a Swagger 2.0 spec: every operation gains a
// required `region` query parameter, the title is suffixed, the description
// documents routing, and vendor extensions record the precedence and the
// valid region list. Definitions are untouched.
func InjectRegionParam(spec []byte, validRegions []string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("swagger spec is not valid JSON: %w", err)
	}

	pathsRaw, ok := doc["paths"]
	if !ok {
		return nil, fmt.Errorf("swagger spec has no paths")
	}
	var paths map[string]json.RawMessage
	if err := json.Unmarshal(pathsRaw, &paths); err != nil {
		return nil, fmt.Errorf("swagger paths: %w", err)
	}

	for pathName, itemRaw := range paths {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(itemRaw, &item); err != nil {
			continue // not an operation container (e.g. $ref) — leave as is
		}
		for method, opRaw := range item {
			if !httpMethods[method] {
				continue
			}
			var op map[string]json.RawMessage
			if err := json.Unmarshal(opRaw, &op); err != nil {
				continue
			}
			if !operationHasQueryParam(op) {
				op["parameters"] = appendParam(op["parameters"])
			}
			updated, err := json.Marshal(op)
			if err != nil {
				return nil, err
			}
			item[method] = updated
		}
		pi, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		paths[pathName] = pi
	}
	pathsJSON, err := json.Marshal(paths)
	if err != nil {
		return nil, err
	}
	doc["paths"] = pathsJSON

	patchInfo(doc)
	extensions(doc, validRegions)

	return json.Marshal(doc)
}

// operationHasQueryParam reports whether the operation already declares a
// query parameter named "region".
func operationHasQueryParam(op map[string]json.RawMessage) bool {
	paramsRaw, ok := op["parameters"]
	if !ok {
		return false
	}
	var params []map[string]json.RawMessage
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		return false
	}
	for _, p := range params {
		var name, in string
		_ = json.Unmarshal(p["name"], &name)
		_ = json.Unmarshal(p["in"], &in)
		if name == "region" && in == "query" {
			return true
		}
	}
	return false
}

func appendParam(paramsRaw json.RawMessage) json.RawMessage {
	var params []json.RawMessage
	if len(paramsRaw) > 0 {
		if err := json.Unmarshal(paramsRaw, &params); err != nil {
			params = nil
		}
	}
	params = append(params, json.RawMessage(regionParam))
	out, err := json.Marshal(params)
	if err != nil {
		return paramsRaw
	}
	return out
}

func patchInfo(doc map[string]json.RawMessage) {
	infoRaw, ok := doc["info"]
	if !ok {
		return
	}
	var info map[string]json.RawMessage
	if err := json.Unmarshal(infoRaw, &info); err != nil {
		return
	}
	var title string
	_ = json.Unmarshal(info["title"], &title)
	if title != "" {
		t, _ := json.Marshal(title + " (core gateway)")
		info["title"] = t
	}
	var desc string
	_ = json.Unmarshal(info["description"], &desc)
	routingNote := " Requests are routed by pipeline-core-service: a `region` is required on every request " +
		"and is stripped before forwarding to the regional Kubeflow Pipelines apiserver."
	d, _ := json.Marshal(desc + routingNote)
	info["description"] = d
	out, err := json.Marshal(info)
	if err != nil {
		return
	}
	doc["info"] = out
}

func extensions(doc map[string]json.RawMessage, validRegions []string) {
	precedence, _ := json.Marshal([]string{
		"header:X-Region",
		"query:region",
		"body:region (top-level, JSON only)",
	})
	doc["x-region-precedence"] = precedence
	if validRegions == nil {
		validRegions = []string{}
	}
	regions, _ := json.Marshal(validRegions)
	doc["x-region-valid"] = regions
}
