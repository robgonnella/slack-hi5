package bier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Params struct {
	Token       string
	ResponseURL string
	Location    string
	Radius      int
	UserName    string
	Category    string
}

type TextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type AccessoryBlock struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
	AltText  string `json:"alt_text"`
}

type Block struct {
	Type      string          `json:"type"`
	Text      *TextBlock      `json:"text,omitempty"`
	Accessory *AccessoryBlock `json:"accessory,omitempty"`
}

type SlackMessage struct {
	ResponseType string  `json:"response_type"`
	Blocks       []Block `json:"blocks"`
}

type Business struct {
	Name        string `json:"name"`
	ImageURL    string `json:"image_url"`
	URL         string `json:"url"`
	ReviewCount int    `json:"review_count"`
	Price       string `json:"price"`
	Rating      float32
	Location    struct {
		DisplayAddress []string `json:"display_address"`
	} `json:"location"`
}

type YelpResponse struct {
	Businesses []Business `json:"businesses"`
}

// Only allow request from this domain
const slackOrigin = "hooks.slack.com"

// Yelp business search base uri
const apiBase = "https://api.yelp.com/v3/businesses/search"

// Env vars
var slackToken = os.Getenv("SLACK_TOKEN")
var apiKey = os.Getenv("API_KEY")

func postToSlack(url string, blocks []Block) error {
	log.Println("Posting message to slack")

	body := SlackMessage{
		ResponseType: "in_channel",
		Blocks:       blocks,
	}
	data, err := json.Marshal(body)
	if err != nil {
		fmt.Printf("Failed to marshal json: %s", err.Error())
		return err
	}

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(data))
	req.Header.Add("Content-Type", "application/json")

	client := http.Client{}
	_, err = client.Do(req)
	if err != nil {
		fmt.Printf("Failed to post to slack: %s", err.Error())
		return err
	}
	return nil
}

func buildBusinessBlocks(params *Params, businesses []Business) []Block {
	log.Println("Building business blocks")

	blocks := []Block{
		Block{
			Type: "section",
			Text: &TextBlock{
				"mrkdwn",
				fmt.Sprintf("*Ok @%s, here are the results for %s near %s*", params.UserName, params.Category, params.Location),
			},
		},
		Block{
			Type: "divider",
		},
	}
	for _, b := range businesses {
		text := fmt.Sprintf(
			"*%s %s:* %.1f Stars (%d reviews)\n%s\n\n%s",
			b.Name,
			b.Price,
			b.Rating,
			b.ReviewCount,
			strings.Join(b.Location.DisplayAddress, " "),
			b.URL,
		)
		blocks = append(blocks,
			Block{
				Type:      "section",
				Text:      &TextBlock{"mrkdwn", text},
				Accessory: &AccessoryBlock{"image", b.ImageURL, "alt text"},
			},
		)
	}
	return blocks
}

func postNotFound(params *Params) error {
	log.Printf("Did not find any results for %s", params.Category)

	msg := fmt.Sprintf(
		"*Sorry we couldn't find any results for %s in %s. "+
			"Try increasing your search radius*",
		params.Category,
		params.Location,
	)
	blocks := []Block{
		{
			Type: "section",
			Text: &TextBlock{
				Type: "mrkdwn",
				Text: msg,
			},
		},
	}
	return postToSlack(params.ResponseURL, blocks)
}

func getYelpResults(params *Params) ([]Business, error) {
	log.Println("Calling yelp api")

	yelpReq, _ := http.NewRequest("GET", apiBase, bytes.NewBuffer([]byte("")))
	yelpReq.Header.Add("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	yelpReq.Header.Add("Content-Type", "application/json")

	q := yelpReq.URL.Query()
	q.Add("location", params.Location)
	q.Add("radius", fmt.Sprintf("%d", params.Radius))
	q.Add("categories", params.Category)
	q.Add("limit", "5")
	q.Add("sort_by", "rating")
	yelpReq.URL.RawQuery = q.Encode()

	client := http.Client{}
	resp, err := client.Do(yelpReq)
	if err != nil {
		return nil, err
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var data YelpResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return data.Businesses, nil
}

func parseParams(params url.Values) (*Params, error) {
	log.Println("Parsing params")

	token := params.Get("token")
	responseURL := params.Get("response_url")
	userName := params.Get("user_name")
	text := strings.TrimSpace(params.Get("text"))
	radiusMi := 5.0

	parts := strings.Split(text, ",")
	if len(parts) < 2 {
		return nil, errors.New("Must provide at least category and location")
	}

	categoryRaw := strings.TrimSpace(parts[0])
	category := strings.ToLower(strings.Join(strings.Split(categoryRaw, " "), ""))
	location := strings.TrimSpace(parts[1])

	if len(parts) > 2 {
		radRaw := strings.TrimSpace(parts[2])
		rad, err := strconv.ParseFloat(radRaw, 64)
		if err == nil {
			radiusMi = rad
		}
	}

	if radiusMi > 24 {
		return nil, errors.New("Maximum radius is 24 miles")
	}

	//convert miles to meters
	radius := int(radiusMi / 0.00062137)
	return &Params{token, responseURL, location, radius, userName, category}, nil
}

func Yelp(w http.ResponseWriter, r *http.Request) {
	log.Println("Request received")

	// Set CORS headers for the preflight request
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", slackOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Max-Age", "3600")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Set main request headers.
	w.Header().Set("Access-Control-Allow-Origin", slackOrigin)

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Println("Failed to read request body")
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	bodyValues, err := url.ParseQuery(fmt.Sprintf("%s", body))
	if err != nil {
		log.Println("Failed to decode body query string")
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	params, err := parseParams(bodyValues)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(err.Error()))
		return
	}

	if params.Token != slackToken {
		log.Println("Unauthorized request")
		return
	}

	// Immediately let slack know we have a valid request
	w.WriteHeader(http.StatusOK)

	businesses, err := getYelpResults(params)
	if err != nil {
		log.Printf("Error getting %s data: %s", params.Category, err.Error())
		w.Write([]byte("Internal Server Error"))
		return
	}
	if len(businesses) == 0 {
		if err := postNotFound(params); err != nil {
			log.Printf("Failed to send empty list message to slack: %s", err.Error())
			w.Write([]byte("Internal Server Error"))
		}
		return
	}

	blocks := buildBusinessBlocks(params, businesses)
	if err := postToSlack(params.ResponseURL, blocks); err != nil {
		log.Printf("Failed to post %s results to slack: %s", params.Category, err.Error())
		w.Write([]byte("Internal Server Error"))
		return
	}
}
