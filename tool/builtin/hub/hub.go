package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
	Todo      int64  `json:"todo,omitempty" jsonschema:"Id of the todo this conversation works on: it is linked to the conversation and marked handed."`
}

func (StartArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["workspace"].MinLength = jsonschema.Ptr(1)
	s.Properties["title"].MinLength, s.Properties["title"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(120)
	s.Properties["message"].MinLength = jsonschema.Ptr(1)
	s.Properties["todo"].Minimum = jsonschema.Ptr(1.0)
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
	Todo         *Todo  `json:"todo,omitempty"`
}

type AddTodoArgs struct {
	Title string `json:"title" jsonschema:"What is to be done, in a line."`
	Notes string `json:"notes,omitempty"`
}

func (AddTodoArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["title"].MinLength, s.Properties["title"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(200)
}

type UpdateTodoArgs struct {
	ID           int64              `json:"id"`
	Title        string             `json:"title,omitempty"`
	Status       account.TodoStatus `json:"status,omitempty"`
	Notes        string             `json:"notes,omitempty" jsonschema:"Replaces the notes."`
	Result       string             `json:"result,omitempty" jsonschema:"What came of it, in a sentence."`
	Conversation string             `json:"conversation,omitempty" jsonschema:"Id of a conversation that works on it, such as one the user started; it and its workspace are linked to the todo."`
}

func (UpdateTodoArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["id"].Minimum = jsonschema.Ptr(1.0)
	s.Properties["title"].MaxLength = jsonschema.Ptr(200)
	s.Properties["status"].Enum = todoStatuses()
}

func todoStatuses() []any {
	var out []any
	for _, st := range account.TodoStatuses {
		out = append(out, string(st))
	}
	return out
}

type ListTodosArgs struct {
	Statuses []account.TodoStatus `json:"statuses,omitempty" jsonschema:"The statuses to list; open and handed when left out."`
}

func (ListTodosArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["statuses"].Items.Enum = todoStatuses()
}

type Todo struct {
	ID           int64              `json:"id"`
	Title        string             `json:"title"`
	Workspace    string             `json:"workspace,omitempty"`
	Conversation string             `json:"conversation,omitempty"`
	Status       account.TodoStatus `json:"status"`
	Notes        string             `json:"notes,omitempty"`
	Result       string             `json:"result,omitempty"`
	Created      string             `json:"created"`
	Updated      string             `json:"updated"`
	Done         string             `json:"done,omitempty"`
}

type Todos struct {
	Todos []Todo `json:"todos"`
}

func todoOf(t account.Todo) Todo {
	out := Todo{ID: t.ID, Title: t.Title, Workspace: t.Workspace, Conversation: t.Thread, Status: t.Status, Notes: t.Notes, Result: t.Result,
		Created: t.Created.Format(minute), Updated: t.Updated.Format(minute)}
	if t.Done != nil {
		out.Done = t.Done.Format(minute)
	}
	return out
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
			Name:        agent.AddTodoTool,
			Description: "Records a todo of the user's, open until it is handed out. Returns it with its id.",
			InputSchema: kit.InputSchema[AddTodoArgs](),
			Annotations: tool.Annotations{Title: "Add a todo", OpenWorldHint: new(false)},
		},
		{
			Name: agent.UpdateTodoTool,
			Description: "Changes a todo: its title, status (open, handed, done, dropped), notes, result, or the conversation that works on it. " +
				"Fields left out stay as they are; marking it done records when.",
			InputSchema: kit.InputSchema[UpdateTodoArgs](),
			Annotations: tool.Annotations{Title: "Update a todo", OpenWorldHint: new(false)},
		},
		{
			Name:        agent.ListTodosTool,
			Description: "Lists the user's todos, the open and handed ones unless statuses says otherwise.",
			InputSchema: kit.InputSchema[ListTodosArgs](),
			Annotations: tool.Annotations{Title: "List todos", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)},
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
	case agent.AddTodoTool:
		return p.addTodo(ctx, c)
	case agent.UpdateTodoTool:
		return p.updateTodo(ctx, c)
	case agent.ListTodosTool:
		return p.listTodos(ctx, c)
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
	var from store.Thread
	err = p.St.Read(ctx, func(r store.Reader) (err error) {
		if from, err = r.Thread(c.Thread); err != nil {
			return err
		}
		w, err = r.Workspace(c.User, args.Workspace)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return kit.Result{}, fmt.Errorf("the user has no workspace %q; %s lists them", args.Workspace, agent.ListWorkspacesTool)
	}
	if err != nil {
		return kit.Result{}, err
	}
	if w.Kind == account.HubWorkspace {
		return kit.Result{}, fmt.Errorf("workspace %s is a hub; hand work only to a project workspace", w.Name)
	}
	if args.Todo != 0 {
		if err := p.St.Read(ctx, func(r store.Reader) error { _, err := r.Todo(c.User, args.Todo); return err }); err != nil {
			return kit.Result{}, fmt.Errorf("the user has no todo %d: %w", args.Todo, err)
		}
	}
	th, err := p.App.StartThread(ctx, c.User, w.ID, "", args.Title)
	if err != nil {
		return kit.Result{}, err
	}
	turn, inProgress, err := p.App.Send(ctx, c.User, th.ID, app.Input{Text: marked(from, args.Message)})
	if err != nil {
		return kit.Result{}, fmt.Errorf("conversation %s was started, but its first message was not sent: %w", th.ID, err)
	}
	out := Started{Conversation: th.ID, Turn: turn.ID, Queued: !inProgress}
	if args.Todo == 0 {
		return kit.Result{Output: out}, nil
	}
	todo, err := p.changeTodo(ctx, c.User, args.Todo, func(_ store.Reader, t *account.Todo) error {
		t.Workspace, t.Thread, t.Status = w.ID, th.ID, account.TodoHanded
		return nil
	})
	if err != nil {
		return kit.Result{}, fmt.Errorf("conversation %s was started, but todo %d was not linked to it: %w", th.ID, args.Todo, err)
	}
	out.Todo = &todo
	return kit.Result{Output: out}, nil
}

func (p *Provider) addTodo(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, err := kit.DecodeArguments[AddTodoArgs](c.Arguments)
	if err != nil {
		return kit.Result{}, err
	}
	now := time.Now().UTC()
	var added account.Todo
	err = p.St.Update(ctx, func(tx store.Tx) (err error) {
		added, err = tx.AddTodo(c.User, account.Todo{Title: strings.TrimSpace(args.Title), Status: account.TodoOpen, Notes: args.Notes,
			Created: now, Updated: now})
		return err
	})
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: todoOf(added)}, nil
}

func (p *Provider) updateTodo(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, err := kit.DecodeArguments[UpdateTodoArgs](c.Arguments)
	if err != nil {
		return kit.Result{}, err
	}
	if args.Status != "" && !slices.Contains(account.TodoStatuses, args.Status) {
		return kit.Result{}, fmt.Errorf("a todo is one of %q, not %q", account.TodoStatuses, args.Status)
	}
	todo, err := p.changeTodo(ctx, c.User, args.ID, func(r store.Reader, t *account.Todo) error {
		if args.Conversation != "" {
			th, err := owned(r, c.User, args.Conversation)
			if err != nil {
				return err
			}
			t.Workspace, t.Thread = th.Workspace, th.ID
		}
		if title := strings.TrimSpace(args.Title); title != "" {
			t.Title = title
		}
		if args.Status != "" {
			t.Status = args.Status
		}
		if args.Notes != "" {
			t.Notes = args.Notes
		}
		if args.Result != "" {
			t.Result = args.Result
		}
		return nil
	})
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: todo}, nil
}

func (p *Provider) changeTodo(ctx context.Context, user string, id int64, change func(store.Reader, *account.Todo) error) (Todo, error) {
	var t account.Todo
	err := p.St.Update(ctx, func(tx store.Tx) (err error) {
		if t, err = tx.Todo(user, id); errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("the user has no todo %d", id)
		}
		if err != nil {
			return err
		}
		wasDone := t.Status == account.TodoDone
		if err := change(tx, &t); err != nil {
			return err
		}
		t.Updated = time.Now().UTC()
		switch done := t.Status == account.TodoDone; {
		case done && !wasDone:
			t.Done = &t.Updated
		case !done:
			t.Done = nil
		}
		return tx.PutTodo(user, t)
	})
	return todoOf(t), err
}

func (p *Provider) listTodos(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, err := kit.DecodeArguments[ListTodosArgs](c.Arguments)
	if err != nil {
		return kit.Result{}, err
	}
	statuses := args.Statuses
	if len(statuses) == 0 {
		statuses = []account.TodoStatus{account.TodoOpen, account.TodoHanded}
	}
	var all []account.Todo
	if err := p.St.Read(ctx, func(r store.Reader) (err error) { all, err = r.Todos(c.User, statuses); return err }); err != nil {
		return kit.Result{}, err
	}
	out := Todos{Todos: []Todo{}}
	for _, t := range all {
		out.Todos = append(out.Todos, todoOf(t))
	}
	return kit.Result{Output: out}, nil
}

func (p *Provider) send(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, err := kit.DecodeArguments[SendArgs](c.Arguments)
	if err != nil {
		return kit.Result{}, err
	}
	var from, to store.Thread
	var w account.Workspace
	err = p.St.Read(ctx, func(r store.Reader) (err error) {
		if from, err = r.Thread(c.Thread); err != nil {
			return err
		}
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
	turn, inProgress, err := p.App.Send(ctx, c.User, to.ID, app.Input{Text: marked(from, args.Message)})
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: Started{Conversation: to.ID, Turn: turn.ID, Queued: !inProgress}}, nil
}

func marked(from store.Thread, message string) string {
	return fmt.Sprintf("[Sent by the hub conversation %q (%s) on the user's behalf]\n\n%s", from.Title, from.ID, message)
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
			turn.Messages = append(turn.Messages, Message{Kind: it.Kind, Text: clip(text, messageRunes)})
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
