package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"

	"myapp/assets"
	"myapp/ui/pages"

	"github.com/a-h/templ"
	"github.com/joho/godotenv"
	"github.com/templui/templui/components/toast"
	"github.com/templui/templui/utils"
)

func main() {
	initDotEnv()

	mux := http.NewServeMux()
	setupAssetsRoutes(mux)
	
	mux.HandleFunc("GET /login", handleLoginView)
	mux.HandleFunc("POST /login", handleLoginAction)
	mux.HandleFunc("POST /logout", handleLogout)
	
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if !isAuthenticated(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		templ.Handler(pages.Preview()).ServeHTTP(w, r)
	})
	
	mux.HandleFunc("POST /preview", func(w http.ResponseWriter, r *http.Request) {
		if !isAuthenticated(r) {
			w.Header().Set("HX-Redirect", "/login")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		handlePreviewRequest(w, r)
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	fmt.Printf("Server is running on http://localhost:%s\n", port)
	err := http.ListenAndServe(":"+port, mux)
	if err != nil {
		panic(err)
	}
}

func initDotEnv() {
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Error loading .env file")
	}
}

func setupAssetsRoutes(mux *http.ServeMux) {
	isDevelopment := os.Getenv("GO_ENV") != "production"

	// Your app assets (CSS, fonts, images, ...)
	assetHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isDevelopment {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000")
		}

		var fs http.Handler
		if isDevelopment {
			fs = http.FileServer(http.Dir("./assets"))
		} else {
			fs = http.FileServer(http.FS(assets.Assets))
		}

		fs.ServeHTTP(w, r)
	})

	mux.Handle("GET /assets/", http.StripPrefix("/assets/", assetHandler))

	// templUI embedded component scripts
	utils.SetupScriptRoutes(mux, isDevelopment)
}

func handlePreviewRequest(w http.ResponseWriter, r *http.Request) {
	// Parse the incoming multipart form
	err := r.ParseMultipartForm(10 << 20) // 10 MB
	if err != nil {
		http.Error(w, fmt.Sprintf("Error parsing form: %v", err), http.StatusBadRequest)
		return
	}

	templateFile, header, err := r.FormFile("template")
	if err != nil {
		http.Error(w, "Template file is required", http.StatusBadRequest)
		return
	}
	defer templateFile.Close()

	jsonData := r.FormValue("jsonData")
	if jsonData == "" {
		http.Error(w, "jsonData is required", http.StatusBadRequest)
		return
	}

	// Create a new multipart body for the outgoing request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add the file part
	part, err := writer.CreateFormFile("template", header.Filename)
	if err != nil {
		http.Error(w, "Error creating form file", http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(part, templateFile)
	if err != nil {
		http.Error(w, "Error copying file data", http.StatusInternalServerError)
		return
	}

	// Add the json data part
	err = writer.WriteField("jsonData", jsonData)
	if err != nil {
		http.Error(w, "Error writing json data", http.StatusInternalServerError)
		return
	}

	err = writer.Close()
	if err != nil {
		http.Error(w, "Error closing writer", http.StatusInternalServerError)
		return
	}

	// Send to microservice
	proxyReq, err := http.NewRequest("POST", "https://go-microservice-production.up.railway.app/v1/files/preview", body)
	if err != nil {
		http.Error(w, "Error creating proxy request", http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Content-Type", writer.FormDataContentType())
	proxyReq.Header.Set("X-Server-ID", "calculator-server")
	proxyReq.Header.Set("X-PIN", "123")

	client := &http.Client{}
	resp, err := client.Do(proxyReq)
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		
		// Styled error message for the pane
		errorHtml := fmt.Sprintf(`<div class="p-4 text-red-500 bg-red-100/10 rounded border border-red-500/20"><strong>Network Error:</strong><br/>%v</div>`, err)
		w.Write([]byte(errorHtml))
		
		// Toast notification (OOB swap)
		fmt.Fprintf(w, `<div id="toast-container" hx-swap-oob="true">`)
		toast.Toast(toast.Props{
			Title:       "Network Error",
			Description: err.Error(),
			Variant:     toast.VariantError,
			Position:    toast.PositionTopRight,
			Duration:    5000,
			Dismissible: true,
		}).Render(r.Context(), w)
		fmt.Fprintf(w, `</div>`)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "Error reading response body", http.StatusInternalServerError)
		return
	}

	contentType := resp.Header.Get("Content-Type")

	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(resp.StatusCode)
		
		// Styled error message for the pane
		errorHtml := fmt.Sprintf(`<div class="p-4 text-red-500 bg-red-100/10 rounded border border-red-500/20 w-full"><strong>Microservice Error (%d):</strong><br/><pre class="whitespace-pre-wrap mt-2 text-sm text-black bg-white/50 p-2 rounded">%s</pre></div>`, resp.StatusCode, string(respBody))
		w.Write([]byte(errorHtml))
		
		// Toast notification (OOB swap)
		fmt.Fprintf(w, `<div id="toast-container" hx-swap-oob="true">`)
		toast.Toast(toast.Props{
			Title:       fmt.Sprintf("Microservice Error (%d)", resp.StatusCode),
			Description: "Check the preview pane for details.",
			Variant:     toast.VariantError,
			Position:    toast.PositionTopRight,
			Duration:    5000,
			Dismissible: true,
		}).Render(r.Context(), w)
		fmt.Fprintf(w, `</div>`)
		return
	}

	if contentType == "application/pdf" {
		// Base64 encode and return object snippet
		b64 := base64.StdEncoding.EncodeToString(respBody)
		objHtml := fmt.Sprintf(`<object data="data:application/pdf;base64,%s" type="application/pdf" class="w-full h-full"></object>`, b64)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(objHtml))
	} else {
		// Return directly as HTML
		w.Header().Set("Content-Type", contentType)
		w.Write(respBody)
	}
}

func isAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie("pdf_proxy_session")
	return err == nil && cookie.Value == "authenticated"
}

func handleLoginView(w http.ResponseWriter, r *http.Request) {
	if isAuthenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	pages.Login("").Render(r.Context(), w)
}

func handleLoginAction(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "vicjocaso" && password == "earwokjgherwuig" {
		http.SetCookie(w, &http.Cookie{
			Name:     "pdf_proxy_session",
			Value:    "authenticated",
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   3600 * 24, // 24 hours
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	pages.Login("Invalid username or password").Render(r.Context(), w)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "pdf_proxy_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
