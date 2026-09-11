// Package swagger serves the gateway's Swagger UI and the region-annotated
// API spec. Paths mirror the upstream apiserver (/swagger, /swagger/
// swagger.json, /swagger/v2beta1/swagger.json) so existing generators
// (e.g. the Bruno collection script) work against the gateway unchanged.
package swagger

import (
	"net/http"
)

// Handler serves the UI and the spec provided by SpecProvider.
func Handler(provider *SpecProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/swagger":
			http.Redirect(w, r, "/swagger/", http.StatusFound)
		case "/swagger/", "/swagger/index.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(swaggerHTML))
		case "/swagger/swagger.json", "/swagger/v2beta1/swagger.json":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(provider.Current())
		default:
			http.NotFound(w, r)
		}
	})
}

// swaggerHTML mirrors the apiserver's swagger_handler.go UI.
const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>KFP Core Service API - Swagger UI</title>
    <link rel="stylesheet" type="text/css" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.10.5/swagger-ui.css">
    <style>
        html { box-sizing: border-box; overflow: -moz-scrollbars-vertical; overflow-y: scroll; }
        *, *:before, *:after { box-sizing: inherit; }
        body { margin: 0; padding: 0; }
        .topbar { background-color: #1f77b4; padding: 10px 20px; }
        .topbar .link { color: white; font-weight: bold; text-decoration: none; }
    </style>
</head>
<body>
    <div class="topbar">
        <a class="link" href="/">KFP Core Service API Documentation</a>
    </div>
    <div id="swagger-ui"></div>
    <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.10.5/swagger-ui-bundle.js" charset="UTF-8"></script>
    <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.10.5/swagger-ui-standalone-preset.js" charset="UTF-8"></script>
    <script>
    window.onload = function() {
        const ui = SwaggerUIBundle({
            urls: [
                {url: "v2beta1/swagger.json", name: "KFP API v2beta1 (core gateway)"}
            ],
            "urls.primaryName": "KFP API v2beta1 (core gateway)",
            dom_id: '#swagger-ui',
            deepLinking: true,
            presets: [
                SwaggerUIBundle.presets.apis,
                SwaggerUIStandalonePreset
            ],
            plugins: [
                SwaggerUIBundle.plugins.DownloadUrl
            ],
            layout: "StandaloneLayout",
            defaultModelsExpandDepth: 1,
            defaultModelExpandDepth: 1,
            docExpansion: "list",
            filter: true,
            tryItOutEnabled: true,
            persistAuthorization: true,
        });
        window.ui = ui;
    };
    </script>
</body>
</html>
`
