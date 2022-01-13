package xray

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Ehco1996/ehco/internal/logger"
	proxy "github.com/xtls/xray-core/app/proxyman/command"
	stats "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
)

// now only support shadownsocks user,maybe support other protocol later
type User struct {
	running bool

	ID       int    `json:"user_id"`
	Method   int    `json:"method"`
	Password string `json:"password"`

	Level           int   `json:"level"`
	Enable          bool  `json:"enable"`
	UploadTraffic   int64 `json:"upload_traffic"`
	DownloadTraffic int64 `json:"download_traffic"`
}

// NOTE we user user id as email
func (u *User) GetEmail() string {
	return string(u.ID)
}

func (u *User) ResetTraffic() {
	u.DownloadTraffic = 0
	u.UploadTraffic = 0
}

// UserPool user pool
type UserPool struct {
	sync.RWMutex
	// map key : ID
	users map[int]*User

	httpClient  *http.Client
	proxyClient proxy.HandlerServiceClient
	statsClient stats.StatsServiceClient
}

// NewUserPool New UserPool
func NewUserPool(ctx context.Context, xrayEndPoint string) (*UserPool, error) {
	conn, err := grpc.DialContext(ctx, xrayEndPoint, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		return nil, err
	}

	// Init Client
	proxyClient := proxy.NewHandlerServiceClient(conn)
	statsClient := stats.NewStatsServiceClient(conn)
	httpClient := http.Client{Timeout: 30 * time.Second}

	up := &UserPool{
		users: make(map[int]*User),

		httpClient:  &httpClient,
		proxyClient: proxyClient,
		statsClient: statsClient,
	}

	return up, nil
}

// CreateUser get create user
func (up *UserPool) CreateUser(userId, level int, password string, enable bool) *User {
	up.Lock()
	defer up.Unlock()
	u := &User{
		running:  false,
		ID:       userId,
		Password: password,
		Level:    level,
		Enable:   enable,
	}
	up.users[u.ID] = u
	return u
}

func (up *UserPool) GetUser(id int) (*User, error) {
	up.RLock()
	defer up.RUnlock()

	if user, found := up.users[id]; found {
		return user, nil
	} else {
		return nil, errors.New(fmt.Sprintf("User Not Found Id: %s", id))
	}
}

func (up *UserPool) RemoveUser(id int) {
	up.Lock()
	defer up.Unlock()
	delete(up.users, id)
}

func (up *UserPool) GetAllUsers() []*User {
	up.RLock()
	defer up.RUnlock()

	users := make([]*User, 0, len(up.users))
	for _, user := range up.users {
		users = append(users, user)
	}
	return users
}

func (up *UserPool) syncTrafficToServer(ctx context.Context) error {
	// sync traffic from xray server
	// V2ray的stats的统计模块设计的非常奇怪，具体规则如下
	// 上传流量："user>>>" + user.Email + ">>>traffic>>>uplink"
	// 下载流量："user>>>" + user.Email + ">>>traffic>>>downlink"
	resp, err := up.statsClient.QueryStats(ctx, &stats.QueryStatsRequest{Pattern: "user>>>", Reset_: true})
	if err != nil {
		return err
	}

	for _, stat := range resp.Stat {
		userIDStr, trafficType := getEmailAndTrafficType(stat.Name)
		userID, err := strconv.Atoi(userIDStr)
		if err != nil {
			return err
		}
		user, err := up.GetUser(userID)
		if err != nil {
			return err
		}
		switch trafficType {
		case "uplink":
			user.UploadTraffic = stat.Value
		case "downlink":
			user.DownloadTraffic = stat.Value
		}
	}

	tfs := make([]*UserTraffic, 0, len(up.users))
	for _, user := range up.GetAllUsers() {
		tf := user.DownloadTraffic + user.UploadTraffic
		if tf > 0 {
			logger.Infof("[xray] User: %v Now Used Total Traffic: %v", user.ID, tf)
			tfs = append(tfs, &UserTraffic{
				UserId:          user.ID,
				DownloadTraffic: user.DownloadTraffic,
				UploadTraffic:   user.UploadTraffic,
			})
			user.ResetTraffic()
		}
	}
	postJson(up.httpClient, API_ENDPOINT, &syncReq{UserTraffics: tfs})
	logger.Infof("[xray] Call syncUserTrafficToServer ONLINE USER COUNT: %d", len(tfs))
	return nil
}