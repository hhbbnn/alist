package drivers

import (
	"fmt"
	"github.com/Xhofe/alist/conf"
	"github.com/Xhofe/alist/model"
	"github.com/Xhofe/alist/utils"
	"github.com/go-resty/resty/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/robfig/cron/v3"
	log "github.com/sirupsen/logrus"
	"path/filepath"
	"time"
)

var aliClient = resty.New()

func init() {
	RegisterDriver("AliDrive", &AliDrive{})
	aliClient.
		SetRetryCount(3).
		SetHeader("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/87.0.4280.88 Safari/537.36").
		SetHeader("content-type", "application/json").
		SetHeader("origin", "https://aliyundrive.com")
}

type AliDrive struct{}

func (a AliDrive) Items() []Item {
	return []Item{
		{
			Name:     "refresh_token",
			Label:    "refresh token",
			Type:     "string",
			Required: true,
		},
		{
			Name:     "root_folder",
			Label:    "root folder file_id",
			Type:     "string",
			Required: false,
		},
	}
}

func (a AliDrive) Proxy(ctx *fiber.Ctx) {
	ctx.Request().Header.Del("Origin")
	ctx.Request().Header.Set("Referer", "https://www.aliyundrive.com/")
}

type AliRespError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type AliFiles struct {
	Items      []AliFile `json:"items"`
	NextMarker string    `json:"next_marker"`
}

type AliFile struct {
	DriveId       string     `json:"drive_id"`
	CreatedAt     *time.Time `json:"created_at"`
	FileExtension string     `json:"file_extension"`
	FileId        string     `json:"file_id"`
	Type          string     `json:"type"`
	Name          string     `json:"name"`
	Category      string     `json:"category"`
	ParentFileId  string     `json:"parent_file_id"`
	UpdatedAt     *time.Time `json:"updated_at"`
	Size          int64      `json:"size"`
	Thumbnail     string     `json:"thumbnail"`
	Url           string     `json:"url"`
}

func AliToFile(file AliFile) *model.File {
	f := &model.File{
		Name:      file.Name,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
		Thumbnail: file.Thumbnail,
		Driver:    "AliDrive",
	}
	if file.Type == "folder" {
		f.Type = conf.FOLDER
	} else {
		f.Type = utils.GetFileType(file.FileExtension)
	}
	if file.Category == "video" {
		f.Type = conf.VIDEO
	}
	if file.Category == "image" {
		f.Type = conf.IMAGE
	}
	return f
}

func (a AliDrive) GetFiles(fileId string, account *model.Account) ([]AliFile, error) {
	marker := "first"
	res := make([]AliFile, 0)
	for marker != "" {
		if marker == "first" {
			marker = ""
		}
		var resp AliFiles
		var e AliRespError
		_, err := aliClient.R().
			SetResult(&resp).
			SetError(&e).
			SetHeader("authorization", "Bearer\t"+account.AccessToken).
			SetBody(JsonStr(Json{
				"drive_id":                account.DriveId,
				"fields":                  "*",
				"image_thumbnail_process": "image/resize,w_400/format,jpeg",
				"image_url_process":       "image/resize,w_1920/format,jpeg",
				"limit":                   account.Limit,
				"marker":                  marker,
				"order_by":                account.OrderBy,
				"order_direction":         account.OrderDirection,
				"parent_file_id":          fileId,
				"video_thumbnail_process": "video/snapshot,t_0,f_jpg,ar_auto,w_300",
				//"url_expire_sec":          1600,
			})).Post("https://api.aliyundrive.com/v2/file/list")
		if err != nil {
			return nil, err
		}
		if e.Code != "" {
			return nil, fmt.Errorf("%s", e.Message)
		}
		marker = resp.NextMarker
		res = append(res, resp.Items...)
	}
	return res, nil
}

// path: /aaa/bbb
func (a AliDrive) Path(path string, account *model.Account) (*model.File, []*model.File, error) {
	path = utils.ParsePath(path)
	log.Debugf("ali path: %s", path)
	cache, err := conf.Cache.Get(conf.Ctx, fmt.Sprintf("%s%s", account.Name, path))
	if err == nil {
		file, ok := cache.(AliFile)
		if ok {
			return AliToFile(file), nil, nil
		} else {
			files, _ := cache.([]AliFile)
			res := make([]*model.File, 0)
			for _, file = range files {
				res = append(res, AliToFile(file))
			}
			return nil, res, nil
		}
	} else {
		fileId := account.RootFolder
		if path != "/" {
			dir, name := filepath.Split(path)
			dir = utils.ParsePath(dir)
			_, _, err = a.Path(dir, account)
			if err != nil {
				return nil, nil, err
			}
			parentFiles_, _ := conf.Cache.Get(conf.Ctx, fmt.Sprintf("%s%s", account.Name, dir))
			parentFiles, _ := parentFiles_.([]AliFile)
			found := false
			for _, file := range parentFiles {
				if file.Name == name {
					found = true
					if file.Type == "file" {
						return AliToFile(file), nil, nil
					} else {
						fileId = file.FileId
						break
					}
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("path not found")
			}
		}
		files, err := a.GetFiles(fileId, account)
		if err != nil {
			return nil, nil, err
		}
		_ = conf.Cache.Set(conf.Ctx, fmt.Sprintf("%s%s", account.Name, path), files, nil)
		res := make([]*model.File, 0)
		for _, file := range files {
			res = append(res, AliToFile(file))
		}
		return nil, res, nil
	}
}

func (a AliDrive) Link(path string, account *model.Account) (string, error) {
	dir, name := filepath.Split(path)
	dir = utils.ParsePath(dir)
	_, _, err := a.Path(dir, account)
	if err != nil {
		return "", err
	}
	parentFiles_, _ := conf.Cache.Get(conf.Ctx, fmt.Sprintf("%s%s", account.Name, dir))
	parentFiles, _ := parentFiles_.([]AliFile)
	for _, file := range parentFiles {
		if file.Name == name {
			if file.Type == "file" {
				var resp Json
				var e AliRespError
				_, err = aliClient.R().SetResult(&resp).
					SetError(&e).
					SetHeader("authorization", "Bearer\t"+account.AccessToken).
					SetBody(Json{
						"drive_id":   account.DriveId,
						"file_id":    file.FileId,
						"expire_sec": 14400,
					}).Post("https://api.aliyundrive.com/v2/file/get_download_url")
				if err != nil {
					return "", err
				}
				if e.Code != "" {
					return "", fmt.Errorf("%s", e.Message)
				}
				return resp["url"].(string), nil
			} else {
				return "", fmt.Errorf("can't down folder")
			}
		}
	}
	return "", fmt.Errorf("path not found")
}

type AliTokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func AliRefreshToken(refresh string) (string, string, error) {
	url := "https://auth.aliyundrive.com/v2/account/token"
	var resp AliTokenResp
	var e AliRespError
	_, err := aliClient.R().
		//ForceContentType("application/json").
		SetBody(JsonStr(Json{"refresh_token": refresh, "grant_type": "refresh_token"})).
		SetResult(&resp).
		SetError(&e).
		Post(url)
	if err != nil {
		return "", "", err
	}
	log.Debugf("%+v,%+v", resp, e)
	if e.Code != "" {
		return "", "", fmt.Errorf("failed to refresh token: %s", e.Message)
	}
	return resp.RefreshToken, resp.AccessToken, nil
}

func (a AliDrive) Save(account *model.Account, old *model.Account) error {
	if old != nil {
		conf.Cron.Remove(cron.EntryID(old.CronId))
	}
	if account.RootFolder == "" {
		account.RootFolder = "root"
	}
	if account.Limit == 0 {
		account.Limit = 200
	}
	refresh, access, err := AliRefreshToken(account.RefreshToken)
	if err != nil {
		return err
	}
	var resp Json
	_, _ = aliClient.R().SetResult(&resp).
		SetBody("{}").
		SetHeader("authorization", "Bearer\t"+access).
		Post("https://api.aliyundrive.com/v2/user/get")
	log.Debugf("user info: %+v", resp)
	account.DriveId = resp["default_drive_id"].(string)
	account.RefreshToken, account.AccessToken = refresh, access
	cronId, err := conf.Cron.AddFunc("@every 2h", func() {
		name := account.Name
		newAccount, ok := model.GetAccount(name)
		if !ok {
			return
		}
		newAccount.RefreshToken, newAccount.AccessToken, err = AliRefreshToken(newAccount.RefreshToken)
		if err != nil {
			newAccount.Status = err.Error()
		}
		_ = model.SaveAccount(newAccount)
	})
	if err != nil {
		return err
	}
	account.CronId = int(cronId)
	err = model.SaveAccount(*account)
	if err != nil {
		return err
	}
	return nil
}

var _ Driver = (*AliDrive)(nil)
