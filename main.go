package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var apiKey string
var certPath string

const docketID = "NIST-2024-0001"

// -------------------------- utilities

func fetchJSON(url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Add(key, value)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)
}

func rateLimitedRequest(interval time.Duration, fn func(string) ([]string, error), id string) ([]string, error) {
	time.Sleep(interval)
	return fn(id)
}

// ------------------ documents

type Document struct {
	Attributes DocumentAttributes `json:"attributes"`
}

type DocumentAttributes struct {
	ObjectID string `json:"objectId"`
}

func getDocumentObjectIDs() ([]string, error) {
	url := fmt.Sprintf("https://api.regulations.gov/v4/documents?filter[docketId]=%s", docketID)
	headers := map[string]string{"X-Api-Key": apiKey}
	body, err := fetchJSON(url, headers)
	if err != nil {
		return nil, err
	}

	var data Data
	err = json.Unmarshal(body, &data)
	if err != nil {
		return nil, err
	}

	var objectIds []string
	for _, doc := range data.Data {
		objectIds = append(objectIds, doc.Attributes.ObjectID)
	}

	return objectIds, nil
}

// ----------------- comment IDS

type CommentData struct {
	Data []CommentID `json:"data"`
}

type Data struct {
	Data []Document `json:"data"`
}

type CommentID struct {
	ID string `json:"id"`
}

func getCommentIDs(documentID string) ([]string, error) {
	url := fmt.Sprintf("https://api.regulations.gov/v4/comments?filter[commentOnId]=%s&page[size]=250&page[number]=1", documentID)
	headers := map[string]string{"X-Api-Key": apiKey}
	body, err := fetchJSON(url, headers)
	if err != nil {
		return nil, err
	}

	var data CommentData
	err = json.Unmarshal(body, &data)
	if err != nil {
		return nil, err
	}

	var ids []string
	for _, comment := range data.Data {
		ids = append(ids, comment.ID)
	}

	return ids, nil
}

// ---------------------- comments

type Comment struct {
	Data struct {
		Links struct {
			Self string `json:"self"`
		} `json:"links"`
		Attributes struct {
			ID           string `json:"id"`
			FirstName    string `json:"firstName"`
			LastName     string `json:"lastName"`
			Email        string `json:"email"`
			Organization string `json:"organization"`
			Comment      string `json:"comment"`
		} `json:"attributes"`
		ID            string `json:"id"`
		Relationships struct {
			Attachments struct {
				Links struct {
					Related string `json:"related"`
				} `json:"links"`
			} `json:"attachments"`
		} `json:"relationships"`
	} `json:"data"`
}

func getComment(commentID string) (Comment, error) {
	url := fmt.Sprintf("https://api.regulations.gov/v4/comments/%s", commentID)
	headers := map[string]string{"X-Api-Key": apiKey}
	body, err := fetchJSON(url, headers)
	if err != nil {
		return Comment{}, err
	}

	var c Comment
	err = json.Unmarshal(body, &c)
	if err != nil {
		return Comment{}, err
	}

	return c, nil
}

// -------------------- attachments

type Attachment struct {
	FileURL string `json:"fileUrl"`
}

type AttachmentData struct {
	Data []struct {
		Attributes struct {
			FileFormats []struct {
				FileURL string `json:"fileUrl"`
			} `json:"fileFormats"`
		} `json:"attributes"`
	} `json:"data"`
}

func getAttachments(attachmentURL string) ([]string, error) {
	fmt.Printf("called getAttachments with attachmentURL: %s\n", attachmentURL)
	headers := map[string]string{"X-Api-Key": apiKey}
	body, err := fetchJSON(attachmentURL, headers)
	if err != nil {
		return nil, err
	}
	fmt.Printf("attachments: %s\n", body)

	var data AttachmentData
	err = json.Unmarshal(body, &data)
	if err != nil {
		return nil, err
	}

	var attachments []string
	for _, attachment := range data.Data {
		for _, file := range attachment.Attributes.FileFormats {
			attachments = append(attachments, file.FileURL)
		}
	}

	return attachments, nil
}

// ---------------------- composite types for HTML

type CommentWithAttachments struct {
	ID          string
	Attachments []string
	Comment     Comment
}

type DocumentWithComments struct {
	ObjectID string
	Comments []CommentWithAttachments
}

// --------------------- cache

type Cache struct {
	comments map[string]CommentWithAttachments
}

func newCache() *Cache {
	return &Cache{
		comments: make(map[string]CommentWithAttachments),
	}
}

func (c *Cache) updateComment(commentID string, commentWithAttachments CommentWithAttachments) {
	c.comments[commentID] = commentWithAttachments
	log.Printf("New comment added to cache: %s\n", commentID)
}

func (c *Cache) commentExists(commentID string) bool {
	_, exists := c.comments[commentID]
	if exists {
		log.Printf("Comment %s already in cache\n", commentID)
	}
	return exists
}

func updateCache(cache *Cache) {
	documentIDs, err := getDocumentObjectIDs()
	if err != nil {
		log.Fatalf("Error getting documents: %v\n", err)
	}

	for _, docID := range documentIDs {
		commentIDs, err := rateLimitedRequest(500*time.Millisecond, getCommentIDs, docID)
		if err != nil {
			log.Fatalf("Error getting comment IDs for document %s: %v\n", docID, err)
		}

		for _, commentID := range commentIDs {
			if !cache.commentExists(commentID) {
				comment, err := getComment(commentID)
				if err != nil {
					log.Fatalf("Error getting comment %s: %v\n", commentID, err)
				}
				attachments, err := getAttachments(comment.Data.Relationships.Attachments.Links.Related)
				if err != nil {
					log.Fatalf("Error getting attachments for comment %s: %v\n", commentID, err)
				}
				commentWithAttachments := CommentWithAttachments{
					ID:          commentID,
					Attachments: attachments,
					Comment:     comment,
				}
				cache.updateComment(commentID, commentWithAttachments)
			}
		}
	}
}

func printCache(cache *Cache) {
	for commentID, comment := range cache.comments {
		fmt.Printf("Comment %s:\n", commentID)
		fmt.Printf("\tAttachments: %v\n", comment.Attachments)
	}
}

// ---------------------- HTML generation

func generateHTML(cache *Cache) {

	if err := os.MkdirAll("static", 0755); err != nil {
		log.Fatalf("Error creating static directory: %v\n", err)
	}

	file, err := os.Create(filepath.Join("static", "index.html"))
	if err != nil {
		log.Fatalf("Error creating HTML file: %v\n", err)
	}
	defer file.Close()

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		log.Fatalf("Error loading EST time zone: %v\n", err)
	}

	lastUpdated := time.Now().In(loc).Format("2006-01-02 15:04:05 MST")

	html := fmt.Sprintf(`<html>
<head>
	<title>Comments</title>
    <style>
    table {
      width: 100%%;
      border-collapse: collapse;
    }

    th, td {
      border: 1px solid black;
      padding: 8px;
      text-align: left;
    }

    th {
      background-color: #f2f2f2;
    }
    </style>
    <script>
    function copyTableToClipboard() {
        var range = document.createRange();
        range.selectNode(document.getElementById("commentsTable"));
        window.getSelection().removeAllRanges();
        window.getSelection().addRange(range);
        document.execCommand("copy");
        window.getSelection().removeAllRanges();
        alert("Table copied to clipboard!");
    }
    </script>
</head>
<body>
    <p><i>Data last updated: %s</i></p>
    <button onclick="copyTableToClipboard()">Copy HTML Table to Clipboard</button>
	<table id="commentsTable" border="1">
		<tr>
			<th>Comment URL</th>
			<th>Attachments</th>
			<th>First Name</th>
			<th>Last Name</th>
			<th>Email</th>
			<th>Organization</th>
		</tr><br><br>`, lastUpdated)

	var commentList []CommentWithAttachments
	for _, comment := range cache.comments {
		commentList = append(commentList, comment)
	}

	sort.Slice(commentList, func(i, j int) bool {
		return commentList[i].Comment.Data.Links.Self < commentList[j].Comment.Data.Links.Self
	})

	for _, commentWithAttachments := range commentList {
		comment := commentWithAttachments.Comment
		commentURL := fmt.Sprintf("https://www.regulations.gov/comment/%s", comment.Data.ID)
		html += fmt.Sprintf(`<tr>
			<td><a href="%s">%s</a></td>
			<td>`, commentURL, comment.Data.ID)
		for _, attachment := range commentWithAttachments.Attachments {
			filename := attachment[strings.LastIndex(attachment, "/")+1:]
			html += fmt.Sprintf(`<a href="%s">%s</a><br>`, attachment, filename)
		}
		html += fmt.Sprintf(`</td>
			<td>%s</td>
			<td>%s</td>
			<td>%s</td>
			<td>%s</td>
		</tr>`, comment.Data.Attributes.FirstName, comment.Data.Attributes.LastName, comment.Data.Attributes.Email, comment.Data.Attributes.Organization)
	}

	html += `</table>
</body>
</html>`

	_, err = file.WriteString(html)
	if err != nil {
		log.Fatalf("Error writing to HTML file: %v\n", err)
	}

	log.Println("HTML file generated successfully.")
}

// ---------------------- HTTP server

func redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "https://"+r.Host+r.RequestURI, http.StatusMovedPermanently)
}

func startServerHTTPS() {
	http.Handle("/", http.FileServer(http.Dir("static")))
	log.Println("Starting HTTPS server on :443")

	certFile := filepath.Join(certPath, "fullchain.pem")
	keyFile := filepath.Join(certPath, "privkey.pem")

	go func() {
		log.Println("Redirecting HTTP to HTTPS on :80")
		log.Fatal(http.ListenAndServe(":80", http.HandlerFunc(redirectToHTTPS)))
	}()

	err := http.ListenAndServeTLS(":443", certFile, keyFile, nil)
	if err != nil {
		log.Fatalf("ListenAndServeTLS failed: %v", err)
	}
}

func startServer() {
	http.Handle("/", http.FileServer(http.Dir("static")))
	log.Println("Starting server on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ---------------------- main

func main() {
	cache := newCache()
	apiKey = os.Getenv("API_KEY")
	certPath = os.Getenv("CERT_PATH")

	go func() {
		for {
			updateCache(cache)
			generateHTML(cache)
			time.Sleep(5 * time.Minute)
		}
	}()

	startServerHTTPS()
	// startServer()
}
