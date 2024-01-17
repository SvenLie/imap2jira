package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/BrianLeishman/go-imap"
	"github.com/microcosm-cc/bluemonday"
	"github.com/robfig/cron"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
)

var jiraApiVersion string
var cronInterval string
var jiraUrl string
var jiraUser string
var jiraPassword string
var imapServer string
var imapServerPort string
var imapUser string
var imapPassword string
var imapDoneFolder string
var httpClient *http.Client

type AddIssueResponse struct {
	Key string `json:"key"`
}

func main() {
	jiraApiVersion = os.Getenv("JIRA_API_VERSION")
	cronInterval = os.Getenv("CRON")
	jiraUrl = os.Getenv("JIRA_URL")
	jiraUser = os.Getenv("JIRA_USER")
	jiraPassword = os.Getenv("JIRA_PASSWORD")
	imapServer = os.Getenv("IMAP_SERVER")
	imapServerPort = os.Getenv("IMAP_PORT")
	imapUser = os.Getenv("IMAP_USER")
	imapPassword = os.Getenv("IMAP_PASSWORD")
	imapDoneFolder = os.Getenv("IMAP_DONE_FOLDER")
	httpClient = &http.Client{}

	cr := cron.New()
	err := cr.AddFunc(cronInterval, func() {
		run()
	})
	if err != nil {
		log.Fatal(err)
	}
	go cr.Start()
	sig := make(chan os.Signal)
	signal.Notify(sig, os.Interrupt, os.Kill)
	<-sig

}

func jsonEscape(content string) string {
	jsonByte, err := json.Marshal(content)
	if err != nil {
		log.Println(err)
	}
	jsonString := string(jsonByte)
	return jsonString[1 : len(jsonString)-1]
}

func getAddIssueResponse(resp *http.Response) AddIssueResponse {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	response := AddIssueResponse{}
	err = json.Unmarshal(bodyBytes, &response)
	if err != nil {
		log.Fatal(err)
	}

	return response
}

func printErrorFromApi(resp *http.Response) {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	bodyString := string(bodyBytes)
	err = errors.New(bodyString)
	log.Print(err)
}

func sanitizeMailText(mailText string) string {
	sanitizePolicy := bluemonday.UGCPolicy()

	regex, err := regexp.Compile(`[^\w] && [\\]`)
	if err != nil {
		log.Fatal(err)
	}
	plainTextBody := regex.ReplaceAllString(string(mailText), " ")

	return jsonEscape(sanitizePolicy.Sanitize(plainTextBody))
}

func makePostRequest(endpoint string, body string) *http.Response {
	req, err := http.NewRequest("POST", jiraUrl+endpoint, strings.NewReader(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(jiraUser+":"+jiraPassword)))
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}

	return resp
}

func makePostRequestWithFile(endpoint string, filename string, fileContents []byte) *http.Response {
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		log.Fatal(err)
	}
	_, err = part.Write(fileContents)
	if err != nil {
		log.Fatal(err)
	}
	err = writer.Close()
	if err != nil {
		log.Fatal(err)
	}

	req, err := http.NewRequest("POST", jiraUrl+endpoint, body)
	req.Header.Add("X-Atlassian-Token", "no-check")
	req.Header.Add("Content-Type", writer.FormDataContentType())
	req.Header.Add("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(jiraUser+":"+jiraPassword)))
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}

	return resp
}

func makeGetRequest(endpoint string) *http.Response {
	req, err := http.NewRequest("GET", jiraUrl+endpoint, strings.NewReader(""))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(jiraUser+":"+jiraPassword)))
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}

	return resp
}

func replaceQuotationMarks(value string) string {
	value = strings.Replace(value, "\"", "'", -1)
	value = strings.Replace(value, "\\'", "'", -1)

	valueEscapedByte, err := json.Marshal(value)
	if err != nil {
		return value
	}
	valueEscaped := string(valueEscapedByte)

	return valueEscaped[1:len(valueEscaped)-1]
}

func addCommentToIssue(issueNumber string, subject string, sanitizedBody string, sender string) bool {
	content, err := os.ReadFile("/go/src/app/structure_add_comment.json")
	if err != nil {
		log.Fatal(err)
	}
	
	// Convert []byte to string and print to screen
	jsonString := string(content)
	jsonString = strings.Replace(jsonString, "%SUMMARY%", subject+" ("+sender+")", 1)
	jsonString = strings.Replace(jsonString, "%DESCRIPTION%", strings.TrimSpace(sanitizedBody), 1)

	resp := makeGetRequest("/rest/api/" + jiraApiVersion + "/issue/" + issueNumber)

	if resp.StatusCode != 200 {
		printErrorFromApi(resp)
		return false
	} else {
		resp := makePostRequest("/rest/api/"+jiraApiVersion+"/issue/"+issueNumber+"/comment", jsonString)

		if resp.StatusCode != 201 {
			println("Error while adding comment")
			println(sender)
			println(sanitizedBody)
			println(jsonString)
			printErrorFromApi(resp)
			return false
		} else {
			log.Println("Success add comment for issue " + issueNumber)
			return true
		}
	}
}

func addIssue(subject string, sanitizedBody string) string {
	content, err := os.ReadFile("/go/src/app/structure_new_issue.json")
	if err != nil {
		log.Fatal(err)
	}

	// Convert []byte to string and print to screen
	jsonString := string(content)
	jsonString = strings.Replace(jsonString, "%SUMMARY%", subject, 1)
	jsonString = strings.Replace(jsonString, "%DESCRIPTION%", strings.TrimSpace(sanitizedBody), 1)

	resp := makePostRequest("/rest/api/"+jiraApiVersion+"/issue", jsonString)

	var issueNumber string
	if resp.StatusCode != 201 {
		println("Error while adding issue")
		println(jsonString)
		printErrorFromApi(resp)
	} else {
		issueNumber = getAddIssueResponse(resp).Key
		log.Println("Success add issue " + issueNumber)
	}
	return issueNumber
}

func run() {

	// ============================================================
	// Configuration
	log.Println("Connecting to server...")

	// Connect to server
	imapServerPortString, _ := strconv.Atoi(imapServerPort)
	dialer, err := imap.New(imapUser, imapPassword, imapServer, imapServerPortString)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Connected")

	defer func(dialer *imap.Dialer) {
		err := dialer.Close()
		if err != nil {
			log.Fatal(err)
		}
	}(dialer)

	err = dialer.SelectFolder("INBOX")
	if err != nil {
		log.Fatal(err)
	}

	messageUids, err := dialer.GetUIDs("ALL")
	if err != nil {
		log.Fatal(err)
	}

	if len(messageUids) == 0 {
		log.Println("Found no e-mails in inbox.")
		return
	}

	emails, err := dialer.GetEmails(messageUids...)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Found " + strconv.Itoa(len(emails)) + " e-mails in inbox.")

	if len(emails) != 0 {
		for _, value := range emails {
			sanitizedBody := ""
			if value.Text != "" {
				sanitizedBody = replaceQuotationMarks(sanitizeMailText(value.Text))
			} else {
				sanitizedBody = replaceQuotationMarks(sanitizeMailText(value.HTML))
			}

			subject := replaceQuotationMarks(value.Subject)
			isMessageWithIssueNumber, _ := regexp.MatchString("^.*\\[.*-\\d+]$", subject)
			issueNumber := ""
			if isMessageWithIssueNumber {
				issueNumber = subject[strings.LastIndex(subject, "[")+1 : strings.LastIndex(subject, "]")]
			}

			successful := false
			if isMessageWithIssueNumber {
				successful = addCommentToIssue(issueNumber, subject, sanitizedBody, replaceQuotationMarks(value.From.String()))
				if successful {
					err := dialer.MoveEmail(value.UID, imapDoneFolder)
					if err != nil {
						log.Fatal(err)
					}
				}
			} else {
				issueNumber = addIssue(subject, sanitizedBody)

				if issueNumber != "" {
					successful = true
					err := dialer.MoveEmail(value.UID, imapDoneFolder)
					if err != nil {
						log.Fatal(err)
					}
				}
			}

			if len(value.Attachments) > 0 && successful {
				for _, attachment := range value.Attachments {
					makePostRequestWithFile("/rest/api/"+jiraApiVersion+"/issue/"+issueNumber+"/attachments", attachment.Name, attachment.Content)
				}
			}

		}
	}
}
