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
	"github.com/robfig/cron/v3"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
)

type AddIssueResponse struct {
	Key string `json:"key"`
}

func main() {
	cronInterval := os.Getenv("CRON")
	c := cron.New()
	c.AddFunc(cronInterval, func() {
		run()
	})
	go c.Start()
	sig := make(chan os.Signal)
	signal.Notify(sig, os.Interrupt, os.Kill)
	<-sig

}

func jsonEscape(i string) string {
	b, err := json.Marshal(i)
	if err != nil {
		log.Println(err)
	}
	s := string(b)
	return s[1 : len(s)-1]
}

func getAddIssueResponse(resp *http.Response) AddIssueResponse {
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	response := AddIssueResponse{}
	json.Unmarshal(bodyBytes, &response)

	return response
}

func printErrorFromApi(resp *http.Response) {
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	bodyString := string(bodyBytes)
	err = errors.New(bodyString)
	log.Print(err)
}

func getMailBody(p *mail.Part) string {
	sanitizePolicy := bluemonday.UGCPolicy()
	body, _ := ioutil.ReadAll(p.Body)

	regex, err := regexp.Compile(`[^\w] && [\\]`)
	if err != nil {
		log.Fatal(err)
	}
	plainTextBody := regex.ReplaceAllString(string(body), " ")

	return jsonEscape(sanitizePolicy.Sanitize(plainTextBody))
}

func makePostRequest(endpoint string, body string) *http.Response {
	jiraUrl := os.Getenv("JIRA_URL")
	jiraUser := os.Getenv("JIRA_USER")
	jiraPassword := os.Getenv("JIRA_PASSWORD")

	clt := &http.Client{}
	req, err := http.NewRequest("POST", jiraUrl+endpoint, strings.NewReader(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(jiraUser+":"+jiraPassword)))
	resp, err := clt.Do(req)
	if err != nil {
		log.Fatal(err)
	}

	return resp
}

func makePostRequestWithFile(endpoint string, filename string) *http.Response {
	jiraUrl := os.Getenv("JIRA_URL")
	jiraUser := os.Getenv("JIRA_USER")
	jiraPassword := os.Getenv("JIRA_PASSWORD")

	clt := &http.Client{}

	file, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}
	fileContents, err := ioutil.ReadAll(file)
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
	resp, err := clt.Do(req)
	if err != nil {
		log.Fatal(err)
	}

	return resp
}

func makeGetRequest(endpoint string) *http.Response {
	jiraUrl := os.Getenv("JIRA_URL")
	jiraUser := os.Getenv("JIRA_USER")
	jiraPassword := os.Getenv("JIRA_PASSWORD")

	clt := &http.Client{}
	req, err := http.NewRequest("GET", jiraUrl+endpoint, strings.NewReader(""))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(jiraUser+":"+jiraPassword)))
	resp, err := clt.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	return resp
}

func replaceQuotationMarks(value string) string {
	value = strings.Replace(value, "\"", "'", -1)

	return value
}

func setMailAsSeenForService(c *client.Client, currentMail uint32) {
	seqSet := new(imap.SeqSet)
	seqSet.AddRange(currentMail, currentMail)

	if err := c.Store(seqSet, imap.AddFlags, []interface{}{imap.ImportantFlag}, nil); err != nil {
		log.Println("IMAP Message Flag Update Failed")
		log.Println(err)
		os.Exit(1)
	}

	if err := c.Expunge(nil); err != nil {
		log.Println("IMAP Message mark as unseen Failed")
		os.Exit(1)
	}
}

func run() {

	// ============================================================
	// Configuration
	log.Println("Connecting to server...")

	imapServer := os.Getenv("IMAP_SERVER")
	imapServerPort := os.Getenv("IMAP_PORT")
	imapUser := os.Getenv("IMAP_USER")
	imapPassword := os.Getenv("IMAP_PASSWORD")
	jiraApiVersion := os.Getenv("JIRA_API_VERSION")

	// Connect to server
	c, err := client.DialTLS(imapServer+":"+imapServerPort, nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Connected")

	// Don't forget to logout
	defer c.Logout()

	// Login
	if err := c.Login(imapUser, imapPassword); err != nil {
		log.Fatal(err)
	}
	log.Println("Logged in")

	// Select INBOX
	_, err = c.Select("INBOX", false)
	if err != nil {
		log.Fatal(err)
	}

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.ImportantFlag}
	uids, err := c.Search(criteria)
	if err != nil {
		log.Println(err)
	}
	log.Println("Message count for INBOX:", len(uids))

	messageSet := new(imap.SeqSet)
	messageSet.AddNum(uids...)

	var section imap.BodySectionName
	items := []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope}

	messages := make(chan *imap.Message, len(uids))
	err = c.Fetch(messageSet, items, messages)

	currentMessage := -1

	for message := range messages {
		currentMessage = currentMessage + 1
		currentUid := uids[currentMessage]

		r := message.GetBody(&section)
		subject := replaceQuotationMarks(message.Envelope.Subject)

		isMessageWithIssueNumber, _ := regexp.MatchString("^.*\\[.*-\\d+]$", subject)

		issueNumber := ""
		if isMessageWithIssueNumber {
			issueNumber = subject[strings.LastIndex(subject, "[")+1 : strings.LastIndex(subject, "]")]
		}

		mr, err := mail.CreateReader(r)
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
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				log.Fatal(err)
			}

			switch h := p.Header.(type) {
			case *mail.InlineHeader:
				sanitizedBody := replaceQuotationMarks(getMailBody(p))
				contentType, _, _ := h.ContentType()

				if contentType != "text/plain" {
					continue
				}

				if isMessageWithIssueNumber {
					content, err := ioutil.ReadFile("/go/src/app/structure_add_comment.json")
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
							setMailAsSeenForService(c, currentUid)
							log.Println("Success add comment for issue " + issueNumber)
						}
						defer resp.Body.Close()
					}

				} else {
					content, err := ioutil.ReadFile("/go/src/app/structure_new_issue.json")
					if err != nil {
						log.Fatal(err)
					}

					// Convert []byte to string and print to screen
					jsonString := string(content)
					jsonString = strings.Replace(jsonString, "%SUMMARY%", subject, 1)
					jsonString = strings.Replace(jsonString, "%DESCRIPTION%", strings.TrimSpace(sanitizedBody), 1)

					resp := makePostRequest("/rest/api/"+jiraApiVersion+"/issue", jsonString)

					if resp.StatusCode != 201 {
						println("Error while adding issue")
						println(jsonString)
						printErrorFromApi(resp)
					} else {
						issueNumber = getAddIssueResponse(resp).Key
						setMailAsSeenForService(c, currentUid)
						log.Println("Success add issue " + issueNumber)
					}
				}
			case *mail.AttachmentHeader:
				filename, _ := h.Filename()
				log.Println("Found attachment \"" + filename + "\" for issue number " + issueNumber)
				file, err := os.Create("/tmp/" + filename)
				if err != nil {
					log.Fatal(err)
				}
				_, err = io.Copy(file, p.Body)
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
				defer resp.Body.Close()
			}
		}
	}

}
