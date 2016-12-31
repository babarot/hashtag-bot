package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"github.com/nlopes/slack"
	"github.com/patrickmn/go-cache"
	"github.com/robfig/cron"
	"golang.org/x/oauth2"
)

const (
	STATE_OPEN      = "#67C63D"
	STATE_CLOSED    = "#B52003"
	STATE_MERGED    = "#65488D"
	STATE_NOT_FOUND = "#D3D3D3"
)

var c *cache.Cache = cache.New(60*time.Minute, 30*time.Second)
var pattern *regexp.Regexp = regexp.MustCompile("#([0-9]+)")

var (
	repo = flag.String("repo", "", "Specify github.com repository name")
	user = flag.String("user", "", "Specify github.com user name")
)

func main() {
	flag.Parse()
	api := slack.New(os.Getenv("SLACK_TOKEN"))
	os.Exit(run(api))
}

func run(api *slack.Client) int {
	rtm := api.NewRTM()
	go rtm.ManageConnection()

	if c.ItemCount() == 0 {
		resp, err := fetchIssuesFromGitHub(*user, *repo)
		if err != nil {
			log.Print(err)
			return 1
		}
		log.Print(resp)
	}

	cr := cron.New()
	cr.AddFunc("@hourly", func() {
		resp, err := fetchIssuesFromGitHub(*user, *repo)
		if err != nil {
			log.Print(err)
		}
		log.Print("cron: ", resp)
	})
	cr.Start()

	for {
		select {
		case msg := <-rtm.IncomingEvents:
			switch ev := msg.Data.(type) {
			case *slack.HelloEvent:
				log.Print("Connected!")

			case *slack.MessageEvent:
				pat := pattern.FindStringSubmatch(ev.Text)
				if len(pat) > 1 {
					params := getPostMessageParameters(strings.TrimPrefix(pat[1], "#"))
					_, _, err := api.PostMessage(ev.Channel, "", params)
					if err != nil {
						log.Print(err)
						return 1
					}
				}

			case *slack.InvalidAuthEvent:
				log.Print("Invalid credentials")
				return 1
			}
		}
	}
}

func getPostMessageParameters(n string) slack.PostMessageParameters {
	key, found := c.Get(n)
	if !found {
		log.Printf("%s: no such item, fetch all issues again...\n", n)
		fetchIssuesFromGitHub(*user, *repo)
	}
	if key == nil {
		return slack.PostMessageParameters{}
	}

	issue := key.(github.Issue)

	color := STATE_NOT_FOUND
	switch *issue.State {
	case "open":
		color = STATE_OPEN
	case "closed":
		color = STATE_CLOSED
		if issue.PullRequestLinks != nil {
			color = STATE_MERGED
		}
	}

	method := "Pull Requests"
	if issue.PullRequestLinks == nil {
		method = "Issues"
	}

	params := slack.PostMessageParameters{
		Markdown:  true,
		Username:  "hashtag-bot",
		IconEmoji: ":hash:",
	}
	params.Attachments = []slack.Attachment{}
	params.Attachments = append(params.Attachments, slack.Attachment{
		Fallback:   fmt.Sprintf("%d - %s", *issue.Number, *issue.Title),
		Title:      fmt.Sprintf("<%s|%s>", *issue.HTMLURL, *issue.Title),
		Text:       *issue.Body,
		MarkdownIn: []string{"title", "text", "fields", "fallback"},
		Color:      color,
		ThumbURL:   *issue.User.AvatarURL,
		Footer:     "GitHub " + method,
		Ts:         json.Number(fmt.Sprint((*issue.CreatedAt).Unix())),
	})
	return params
}

func fetchIssuesFromGitHub(user, repo string) (string, error) {
	if user == "" || repo == "" {
		return "", errors.New("user/repo invalid format")
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_ACCESS_TOKEN")},
	)
	tc := oauth2.NewClient(oauth2.NoContext, ts)
	githubClient := github.NewClient(tc)

	opt := &github.IssueListByRepoOptions{
		State:       "all",
		ListOptions: github.ListOptions{PerPage: 100},
	}

	n := 0
	for {
		repos, resp, err := githubClient.Issues.ListByRepo(user, repo, opt)
		if err != nil {
			return "", err
		}
		for _, v := range repos {
			c.Set(fmt.Sprintf("%d", *v.Number), *v, cache.DefaultExpiration)
			n++
		}
		if resp.NextPage == 0 {
			break
		}
		opt.ListOptions.Page = resp.NextPage
	}
	return fmt.Sprintf("%d repos fetched in cache", n), nil
}
