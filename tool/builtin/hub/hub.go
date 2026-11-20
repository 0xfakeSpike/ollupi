package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

const minute = "2006-01-02 15:04"

const (
	answerRunes      = 600
	messageRunes     = 4000
	defaultReadTurns = 3
	maxReadTurns     = 20
	maxListed        = 50
)

type Provider struct {
	St  store.Store
	App *app.App
}

func (*Provider) Name() (string, string) {
	return agent.HubProvider, "Hands work to conversations in the user's project workspaces and follows how it stands."
}

type Workspace struct {
	ID   string                `json:"id"`
	Name string                `json:"name"`
	Root string                `json:"root"`
	Kind account.WorkspaceKind `json:"kind"`
}

type StartArgs struct {
	Workspace string `json:"workspace" jsonschema:"Id of the project workspace the conversation works in."`
	Title     string `json:"title" jsonschema:"A short title for the conversation, shown to the user."`
	Message   string `json:"message" jsonschema:"The first message, complete on its own: the conversation knows nothing of this one."`
}

func (StartArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["workspace"].MinLength = jsonschema.Ptr(1)
	s.Properties["title"].MinLength, s.Properties["title"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(120)
	s.Properties["message"].MinLength = jsonschema.Ptr(1)
}

type SendArgs struct {
	Conversation string `json:"conversation" jsonschema:"Id of the conversation to write to."`
	Message      string `json:"message"`
}

func (SendArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["conversation"].MinLength = jsonschema.Ptr(1)
	s.Properties["message"].MinLength = jsonschema.Ptr(1)
}

type ListArgs struct {
	Conversations []string `json:"conversations,omitempty" jsonschema:"Ids of the conversations to look up."`
	Workspace     string   `json:"workspace,omitempty" jsonschema:"Id of a workspace whose conversations to list, most recently updated first."`
}

type ReadArgs struct {
	Conversation string `json:"conversation"`
	Turns        int    `json:"turns,omitempty" jsonschema:"How many of the latest turns to show."`
}

func (ReadArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["conversation"].MinLength = jsonschema.Ptr(1)
	s.Properties["turns"].Minimum, s.Properties["turns"].Maximum = jsonschema.Ptr(1.0), jsonschema.Ptr(float64(maxReadTurns))
	s.Properties["turns"].Description = fmt.Sprintf("How many of the latest turns to show; default %d.", defaultReadTurns)
}

type Workspaces struct {
	Workspaces []Workspace `json:"workspaces"`
}

type Conversations struct {
	Conversations []Status `json:"conversations"`
}

type Turns struct {
	Turns []Turn `json:"turns"`
}

type Started struct {
	Conversation string `json:"conversation"`
	Turn         string `json:"turn"`
	Queued       bool   `json:"queued"`
}

type Status struct {
	ID         string                    `json:"id"`
	Title      string                    `json:"title"`
	Workspace  string                    `json:"workspace,omitempty"`
	Status     conversation.ThreadStatus `json:"status"`
	Queued     int                       `json:"queuedTurns"`
	Updated    string                    `json:"updated"`
	Unread     bool                      `json:"unread"`
	Archived   bool                      `json:"archived,omitempty"`
	LastTurn   *Ended                    `json:"lastTurnEnded,omitempty"`
	LastAnswer string                    `json:"lastAnswer,omitempty"`
}

type Turn struct {
	ID       string    `json:"id"`
	Queued   string    `json:"queued"`
	Ended    *Ended    `json:"ended,omitempty"`
	Messages []Message `json:"messages"`
}

type Ended struct {
	Status conversation.End `json:"status"`
	Reason string           `json:"reason,omitempty"`
	At     string           `json:"at"`
}

func ended(e *conversation.Ending) *Ended {
	if e == nil {
		return nil
	}
	return &Ended{Status: e.Status, Reason: e.Reason, At: e.At.Format(minute)}
}

type Message struct {
	Kind conversation.ItemKind `json:"kind"`
	Text string                `json:"text"`
	From string                `json:"from,omitempty" jsonschema:"Id of the hub conversation that sent this message on the user's behalf; left out when the user wrote it."`
}

func (*Provider) Tools(context.Context) ([]kit.Spec, error) {
	return []kit.Spec{
		{
			Name:        agent.ListWorkspacesTool,
			Description: "Lists the user's workspaces with their ids, names, folders and kinds. Work can be handed only to a project workspace, never to a hub.",
			InputSchema: kit.InputSchema[struct{}](),
			Annotations: tool.Annotations{Title: "List workspaces", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)},
		},
		{
			Name: agent.StartConversationTool,
			Description: "Starts a conversation in a project workspace and sends it the first message, as the user would; the message is marked as sent by this hub. " +
				"The conversation runs as an ordinary one, under its workspace's permissions, and the user can follow and write to it. Returns its id.",
			InputSchema: kit.InputSchema[StartArgs](),
			Annotations: tool.Annotations{Title: "Start a conversation", OpenWorldHint: new(false)},
		},
		{
			Name: agent.SendMessageTool,
			Description: "Sends a message to a conversation in a project workspace, as the user would; the message is marked as sent by this hub. " +
				"It starts a turn there, or waits behind the turns already queued.",
			InputSchema: kit.InputSchema[SendArgs](),
			Annotations: tool.Annotations{Title: "Send a message", OpenWorldHint: new(false)},
		},
		{
			Name: agent.ListConversationsTool,
			Description: "Shows how conversations stand: status (idle, active, waitingOnApproval, waitingOnUserInput), queued turns, how the last turn ended, " +
				"whether the user has read the last answer, and its start. Without arguments it lists the conversations this hub workspace started, in any of its conversations; " +
				"pass conversations or a workspace to look up any of the user's conversations.",
			InputSchema: kit.InputSchema[ListArgs](),
			Annotations: tool.Annotations{Title: "List conversations", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)},
		},
		{
			Name:        agent.ReadConversationTool,
			Description: "Shows the messages of a conversation's latest turns: what was asked, what it answered, and the reports it got.",
			InputSchema: kit.InputSchema[ReadArgs](),
			Annotations: tool.Annotations{Title: "Read a conversation", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)},
		},
	}, nil
}

func (p *Provider) Invoke(ctx context.Context, c tool.Call) (kit.Result, error) {
	switch c.Tool {
	case agent.ListWorkspacesTool:
		return p.workspaces(ctx, c)
	case agent.StartConversationTool:
		return p.start(ctx, c)
	case agent.SendMessageTool:
		return p.send(ctx, c)
	case agent.ListConversationsTool:
		return p.list(ctx, c)
	case agent.ReadConversationTool:
		return p.read(ctx, c)
	}
	return kit.Result{}, fmt.Errorf("%w: %s", kit.ErrUnknownTool, c.Tool)
}

func (p *Provider) workspaces(ctx context.Context, c tool.Call) (kit.Result, error) {
	var all []account.Workspace
	if err := p.St.Read(ctx, func(r store.Reader) (err error) { all, err = r.Workspaces(c.User); return err }); err != nil {
		return kit.Result{}, err
	}
	out := Workspaces{Workspaces: []Workspace{}}
	for _, w := range all {
		if w.Archived == nil {
			out.Workspaces = append(out.Workspaces, Workspace{ID: w.ID, Name: w.Name, Root: w.Root, Kind: w.Kind})
		}
	}
	return kit.Result{Output: out}, nil
}

func (p *Provider) start(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, err := kit.DecodeArguments[StartArgs](c.Arguments)
	if err != nil {
		return kit.Result{}, err
	}
	var w account.Workspace
	err = p.St.Read(ctx, func(r store.Reader) (err error) { w, err = r.Workspace(c.User, args.Workspace); return err })
	if errors.Is(err, store.ErrNotFound) {
		return kit.Result{}, fmt.Errorf("the user has no workspace %q; %s lists them", args.Workspace, agent.ListWorkspacesTool)
	}
	if err != nil {
		return kit.Result{}, err
	}
	if w.Kind == account.HubWorkspace {
		return kit.Result{}, fmt.Errorf("workspace %s is a hub; hand work only to a project workspace", w.Name)
	}
	th, err := p.App.StartThread(ctx, c.User, w.ID, "", args.Title)
	if err != nil {
		return kit.Result{}, err
	}
	turn, inProgress, err := p.App.Send(ctx, c.User, th.ID, app.Input{Text: args.Message, From: c.Thread})
	if err != nil {
		return kit.Result{}, fmt.Errorf("conversation %s was started, but its first message was not sent: %w", th.ID, err)
	}
	return kit.Result{Output: Started{Conversation: th.ID, Turn: turn.ID, Queued: !inProgress}}, nil
}

func (p *Provider) send(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, err := kit.DecodeArguments[SendArgs](c.Arguments)
	if err != nil {
		return kit.Result{}, err
	}
	var to store.Thread
	var w account.Workspace
	err = p.St.Read(ctx, func(r store.Reader) (err error) {
		if to, err = owned(r, c.User, args.Conversation); err != nil {
			return err
		}
		if to.Workspace == "" {
			return nil
		}
		w, err = r.Workspace(c.User, to.Workspace)
		return err
	})
	if err != nil {
		return kit.Result{}, err
	}
	if w.Kind == account.HubWorkspace {
		return kit.Result{}, fmt.Errorf("conversation %s is in the hub workspace %s; write only to conversations in project workspaces", to.ID, w.Name)
	}
	turn, inProgress, err := p.App.Send(ctx, c.User, to.ID, app.Input{Text: args.Message, From: c.Thread})
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: Started{Conversation: to.ID, Turn: turn.ID, Queued: !inProgress}}, nil
}

func (p *Provider) list(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, err := kit.DecodeArguments[ListArgs](c.Arguments)
	if err != nil {
		return kit.Result{}, err
	}
	out := Conversations{Conversations: []Status{}}
	err = p.St.Read(ctx, func(r store.Reader) error {
		ids := args.Conversations
		switch {
		case args.Workspace != "":
			threads, _, err := r.Threads(c.User, store.ThreadFilter{Workspace: &args.Workspace, Limit: maxListed})
			if err != nil {
				return err
			}
			for _, th := range threads {
				ids = append(ids, th.ID)
			}
		case len(ids) == 0:
			calls, err := r.Calls(c.User, c.Workspace, agent.StartConversationTool)
			if err != nil {
				return err
			}
			ids = started(calls)
		}
		for _, id := range ids {
			st, err := status(r, c.User, id)
			if err != nil {
				return err
			}
			out.Conversations = append(out.Conversations, st)
		}
		return nil
	})
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: out}, nil
}

func started(calls []conversation.Item) []string {
	var ids []string
	for _, it := range calls {
		startedOne := it.Status == conversation.Done && it.Result != nil
		if !startedOne {
			continue
		}
		var s Started
		if json.Unmarshal(it.Result.Output, &s) == nil && s.Conversation != "" && !slices.Contains(ids, s.Conversation) {
			ids = append(ids, s.Conversation)
		}
	}
	return ids
}

func status(r store.Reader, user, id string) (Status, error) {
	th, err := owned(r, user, id)
	if err != nil {
		return Status{}, err
	}
	v, err := r.View(id)
	if err != nil {
		return Status{}, err
	}
	unread := th.Replied != nil && (th.Read == nil || th.Read.Before(*th.Replied))
	st := Status{ID: th.ID, Title: th.Title, Workspace: th.Workspace, Status: th.Status, Queued: len(v.Queued()),
		Updated: th.Updated.Format(minute), Unread: unread, Archived: th.Archived != nil}
	if n := len(v.Turns); n > 0 {
		st.LastTurn = ended(v.Turns[n-1].Ended)
	}
	for i := len(v.Items) - 1; i >= 0; i-- {
		if v.Items[i].Kind == conversation.ItemAgentMessage {
			st.LastAnswer = clip(v.Items[i].Text, answerRunes)
			break
		}
	}
	return st, nil
}

func (p *Provider) read(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, err := kit.DecodeArguments[ReadArgs](c.Arguments)
	if err != nil {
		return kit.Result{}, err
	}
	var v conversation.View
	err = p.St.Read(ctx, func(r store.Reader) (err error) {
		if _, err = owned(r, c.User, args.Conversation); err != nil {
			return err
		}
		v, err = r.View(args.Conversation)
		return err
	})
	if err != nil {
		return kit.Result{}, err
	}
	n := args.Turns
	if n == 0 {
		n = defaultReadTurns
	}
	out := Turns{Turns: []Turn{}}
	for _, t := range v.Turns[max(0, len(v.Turns)-n):] {
		turn := Turn{ID: t.ID, Queued: t.QueuedAt.Format(minute), Ended: ended(t.Ended), Messages: []Message{}}
		for _, it := range v.ItemsOf(t.ID) {
			text := it.Text
			switch {
			case it.Kind == conversation.ItemReport && it.Result != nil:
				text = prompt.OutputText(it.Result.Output)
			case it.Kind != conversation.ItemUserMessage && it.Kind != conversation.ItemAgentMessage:
				continue
			}
			msg := Message{Kind: it.Kind, Text: clip(text, messageRunes)}
			if it.From != nil {
				msg.From = it.From.Thread
			}
			turn.Messages = append(turn.Messages, msg)
		}
		out.Turns = append(out.Turns, turn)
	}
	return kit.Result{Output: out}, nil
}

func owned(r store.Reader, user, id string) (store.Thread, error) {
	th, err := r.Thread(id)
	if err == nil && th.User != user {
		err = store.ErrNotFound
	}
	if errors.Is(err, store.ErrNotFound) {
		return store.Thread{}, fmt.Errorf("the user has no conversation %q", id)
	}
	return th, err
}

func clip(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + fmt.Sprintf(" [... %d more characters]", len(runes)-n)
}
