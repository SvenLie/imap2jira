package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"github.com/microcosm-cc/bluemonday"
	"github.com/robfig/cron"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"regexp"
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
var httpClient *http.Client

var bodySection imap.BodySectionName

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
	httpClient = &http.Client{}

	cr := cron.New()
	cr.AddFunc(cronInterval, func() {
		run()
	})
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
	json.Unmarshal(bodyBytes, &response)

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

func getMailBody(p *mail.Part) string {
	sanitizePolicy := bluemonday.UGCPolicy()
	body, _ := io.ReadAll(p.Body)

	regex, err := regexp.Compile(`[^\w] && [\\]`)
	if err != nil {
		log.Fatal(err)
	}
	plainTextBody := regex.ReplaceAllString(string(body), " ")

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

func makePostRequestWithFile(endpoint string, filename string) *http.Response {
	file, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}
	fileContents, err := io.ReadAll(file)
	if err != nil {
		log.Fatal(err)
	}

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		log.Fatal(err)
	}
	part.Write(fileContents)
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
	return value
}

func setMailAsSeenForService(imapClient *client.Client, currentMail uint32) {
	seqSet := new(imap.SeqSet)
	seqSet.AddRange(currentMail, currentMail)

	if err := imapClient.Store(seqSet, imap.AddFlags, []interface{}{imap.ImportantFlag}, nil); err != nil {
		log.Println("IMAP Message Flag Update Failed")
		log.Println(err)
		os.Exit(1)
	}

	if err := imapClient.Expunge(nil); err != nil {
		log.Println("IMAP Message mark as unseen Failed")
		os.Exit(1)
	}
}

func addCommentToIssue(imapClient *client.Client, issueNumber string, subject string, sanitizedBody string, sender *mail.Address, currentUid uint32) {
	content, err := os.ReadFile("/go/src/app/structure_add_comment.json")
	if err != nil {
		log.Fatal(err)
	}
	// Convert []byte to string and print to screen
	jsonString := string(content)
	jsonString = strings.Replace(jsonString, "%SUMMARY%", subject+" ("+sender.Name+" <"+sender.Address+">)", 1)
	jsonString = strings.Replace(jsonString, "%DESCRIPTION%", strings.TrimSpace(sanitizedBody), 1)

	resp := makeGetRequest("/rest/api/" + jiraApiVersion + "/issue/" + issueNumber)

	if resp.StatusCode != 200 {
		printErrorFromApi(resp)
	} else {
		resp := makePostRequest("/rest/api/"+jiraApiVersion+"/issue/"+issueNumber+"/comment", jsonString)

		if resp.StatusCode != 201 {
			println("Error while adding comment")
			println(jsonString)
			printErrorFromApi(resp)
		} else {
			setMailAsSeenForService(imapClient, currentUid)
			log.Println("Success add comment for issue " + issueNumber)
		}
	}
}

func addIssue(imapClient *client.Client, subject string, sanitizedBody string, currentUid uint32) string {
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
		setMailAsSeenForService(imapClient, currentUid)
		log.Println("Success add issue " + issueNumber)
	}
	return issueNumber
}

func addFileToIssue(issueNumber string, headerTypePart *mail.AttachmentHeader, mailPart *mail.Part) {
	filename, _ := headerTypePart.Filename()
	if filename == "" {
		return
	}

	log.Println("Found attachment \"" + filename + "\" for issue number " + issueNumber)
	file, err := os.Create("/tmp/" + filename)
	if err != nil {
		log.Fatal(err)
	}
	_, err = io.Copy(file, mailPart.Body)
	if err != nil {
		log.Fatal(err)
	}

	if issueNumber == "" {
		log.Fatal("Error, no issue number for attachment found")
	}

	resp := makePostRequestWithFile("/rest/api/"+jiraApiVersion+"/issue/"+issueNumber+"/attachments", "/tmp/"+filename)
	if resp.StatusCode != 200 {
		println("Error while adding attachment for issue " + issueNumber)
		printErrorFromApi(resp)
	} else {
		log.Println("Success add attachment for issue " + issueNumber)
	}
}

func getRelevantMessagesAndUids(imapClient *client.Client) (chan *imap.Message, []uint32) {
	// Select INBOX
	_, err := imapClient.Select("INBOX", false)
	if err != nil {
		log.Fatal(err)
	}

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.ImportantFlag}
	uids, err := imapClient.Search(criteria)
	if err != nil {
		log.Println(err)
	}
	log.Println("Message count for INBOX:", len(uids))

	messageSet := new(imap.SeqSet)
	messageSet.AddNum(uids...)

	items := []imap.FetchItem{bodySection.FetchItem(), imap.FetchEnvelope}

	messages := make(chan *imap.Message, len(uids))
	err = imapClient.Fetch(messageSet, items, messages)

	return messages, uids
}

func run() {

	// ============================================================
	// Configuration
	log.Println("Connecting to server...")

	// Connect to server
	imapClient, err := client.DialTLS(imapServer+":"+imapServerPort, nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Connected")

	// Login
	if err := imapClient.Login(imapUser, imapPassword); err != nil {
		log.Fatal(err)
	}
	log.Println("Logged in")

	currentMessage := -1
	messages, uids := getRelevantMessagesAndUids(imapClient)

	for message := range messages {
		currentMessage = currentMessage + 1
		currentUid := uids[currentMessage]

		messageBody := message.GetBody(&bodySection)
		subject := replaceQuotationMarks(message.Envelope.Subject)

		isMessageWithIssueNumber, _ := regexp.MatchString("^.*\\[.*-\\d+]$", subject)

		issueNumber := ""
		if isMessageWithIssueNumber {
			issueNumber = subject[strings.LastIndex(subject, "[")+1 : strings.LastIndex(subject, "]")]
		}

		mr, err := mail.CreateReader(messageBody)
		if err != nil {
			log.Fatal(err)
		}

		header := mr.Header
		senderArray, err := header.AddressList("From")
		if err != nil {
			log.Fatal(err)
		}

		sender := senderArray[0]

		for {
			mailPart, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				log.Fatal(err)
			}

			switch headerTypePart := mailPart.Header.(type) {
			case *mail.InlineHeader:
				sanitizedBody := replaceQuotationMarks(getMailBody(mailPart))
				contentType, _, _ := headerTypePart.ContentType()

				if contentType != "text/plain" {
					continue
				}

				if isMessageWithIssueNumber {
					addCommentToIssue(imapClient, issueNumber, subject, sanitizedBody, sender, currentUid)
				} else {
					issueNumber = addIssue(imapClient, subject, sanitizedBody, currentUid)
				}
			case *mail.AttachmentHeader:
				if issueNumber != "" {
					addFileToIssue(issueNumber, headerTypePart, mailPart)
				}
			}
		}
	}
}
