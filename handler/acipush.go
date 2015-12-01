package handler

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/astaxie/beego/logs"
	"gopkg.in/macaron.v1"

	"github.com/containerops/dockyard/models"
	"github.com/containerops/wrench/setting"
)


var (
	directory     string
	templatedir   string
	uploadcounter int
	newuploadLock sync.Mutex
	uploads       map[int]*models.Upload
	acis          []models.Aci
)

func init() {

	uploads = make(map[int]*models.Upload)

	directory := setting.ImagePath + "/acpool/"
	templatedir := "conf"

	acis, err := listACIs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
	}
}

// The root page. Builds a human-readable list of what ACIs are available,
// and also provides the meta tags for the server for meta discovery.
func RenderListOfACIs(ctx *macaron.Context, log *logs.BeeLogger) {

	os.RemoveAll(path.Join(directory, "tmp"))
	err := os.MkdirAll(path.Join(directory, "tmp"), 0755)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
	}

	t, err := template.ParseFiles(path.Join(templatedir, "acitemplate.html"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
	}

	err = t.Execute(ctx.Resp, struct {
		ServerName string
		ACIs       []models.Aci
		Domain     string
	}{
		ServerName: setting.Domains,
		ACIs:       acis,
		Domain:     setting.ListenMode,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
	}
}

// Sends the gpg public keys specif`ied via the flag to the client
func GetPubkeys(ctx *macaron.Context, log *logs.BeeLogger) (int, []byte) {
	var pubkey []byte
	var err error

	pubkeypath := setting.ImagePath + "/acpool/" + "pubkeys.gpg"
	if pubkey, err = ioutil.ReadFile(pubkeypath); err != nil {
		// TBD: consider to fetch pubkey from other storage medium

		log.Error("[ACI API] Get pubkey file failed: %v", err.Error())
		result, _ := json.Marshal(map[string]string{"message": "Get pubkey file failed"})
		return http.StatusNotFound, result
	}
	return http.StatusOK, pubkey
}

func InitiateUpload(ctx *macaron.Context, log *logs.BeeLogger) (int, []byte) {
	image := ctx.Params(":image")
	if image == "" {
		log.Error("[ACI API]Get image name failed")
		result, _ := json.Marshal(map[string]string{"message": "Get image name failed"})
		return http.StatusNotFound, result
	}

	uploadNum := strconv.Itoa(newUpload(image))

	var prefix string
	prefix = setting.ListenMode+"://" + setting.Domains + "/ac-push" 

	deets := models.InitiateDetails{
		ACIPushVersion: "0.0.1",
		Multipart:      false,
		ManifestURL:    prefix + "/manifest/" + uploadNum,
		SignatureURL:   prefix + "/signature/" + uploadNum,
		ACIURL:         prefix + "/aci/" + uploadNum,
		CompletedURL:   prefix + "/complete/" + uploadNum,
	}

	result, _ := json.Marshal(deets)
	return http.StatusInternalServerError, result

}

func UploadManifest(ctx *macaron.Context, log *logs.BeeLogger) (int) {
	num, err := strconv.Atoi(ctx.Params(":num"))
	if err != nil {
		return http.StatusNotFound
	}

	err = gotMan(num)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
		return http.StatusInternalServerError
	}
	return http.StatusOK
}

func ReceiveSignUpload(ctx *macaron.Context, log *logs.BeeLogger) (int) {
	num, err := strconv.Atoi(ctx.Params(":num"))
	if err != nil {
		return http.StatusNotFound
	}

	up := getUpload(num)
	if up == nil {
		return http.StatusNotFound
	}

	_, err = os.Stat(up.Image)
	if err == nil {
		log.Error("[ACI API]item already uploaded")
		return http.StatusConflict
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "%v", err)
		return http.StatusInternalServerError
	}

	aci, err := os.OpenFile(tmpSigPath(num),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
		return http.StatusInternalServerError
	}
	defer aci.Close()

	_, err = io.Copy(aci, ctx.Req.Request.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
		return http.StatusInternalServerError
	}

	err = gotSig(num)
	if err != nil {
		result, _ := json.Marshal(err)
		return http.StatusInternalServerError, result
	}

	return http.StatusOK
}

func ReceiveAciUpload(ctx *macaron.Context, log *logs.BeeLogger) (int) {
	num, err := strconv.Atoi(ctx.Params(":num"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
		return http.StatusNotFound
	}

	up := getUpload(num)
	if up == nil {
		fmt.Fprintf(os.Stderr, "%v", err)
		return http.StatusNotFound
	}

	_, err = os.Stat(up.Image)
	if err == nil {
		log.Error("[ACI API]item already uploaded")
		return http.StatusConflict
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "%v", err)
		return http.StatusInternalServerError
	}

	aci, err := os.OpenFile(tmpACIPath(num),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
		return http.StatusInternalServerError
	}
	defer aci.Close()

	_, err = io.Copy(aci, ctx.Req.Request.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
		return http.StatusInternalServerError
	}

	err = gotACI(num)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
		return http.StatusInternalServerError
	}
	return http.StatusOK
}

func tmpSigPath(num int) string {
	return path.Join(directory, "tmp", strconv.Itoa(num)+".asc")
}

func tmpACIPath(num int) string {
	return path.Join(directory, "tmp", strconv.Itoa(num))
}

func CompleteUpload(ctx *macaron.Context, log *logs.BeeLogger) (int, []byte) {
	num, err := strconv.Atoi(ctx.Params(":num"))
	if err != nil {
		result, _ := json.Marshal(map[string]string{})
		return http.StatusNotFound, result
	}

	up := getUpload(num)
	if up == nil {
		result, _ := json.Marshal(map[string]string{})
		return http.StatusNotFound, result
	}

	body, err := ioutil.ReadAll(ctx.Req.Request.Body)
	if err != nil {
		result, _ := json.Marshal(map[string]string{})
		return http.StatusInternalServerError, result
	}

	fmt.Fprintf(os.Stderr, "body: %s\n", string(body))

	msg := models.CompleteMsg{}
	err = json.Unmarshal(body, &msg)
	if err != nil {
		log.Error("[ACI API]error unmarshaling json: %v", err.Error())
		result, _ := json.Marshal("error unmarshaling json")
		return http.StatusBadRequest, result
	}

	if !msg.Success {
		err := reportFailure(num)
		if err != nil {
		    log.Error("[ACI API]client reported failure: %v", err.Error())
			status, result := msgFailure("client reported failure", msg.Reason)
			return status, result
		}
	}

	if !up.GotMan {
		err := reportFailure(num)
		if err != nil {
		    log.Error("[ACI API]manifest wasn't uploaded: %v", err.Error())
			status, result := msgFailure("manifest wasn't uploaded", msg.Reason)
			return status, result
		}
	}

	if !up.GotSig {
		err := reportFailure(num)
		if err != nil {
		    log.Error("[ACI API]signature wasn't uploaded: %v", err.Error())
			status, result := msgFailure("signature wasn't uploaded", msg.Reason)
			return status, result
		}
	}

	if !up.GotACI {
		err := reportFailure(num)
		if err != nil {
		    log.Error("[ACI API]ACI wasn't uploaded: %v", err.Error())
			status, result := msgFailure("ACI wasn't uploaded", msg.Reason)
			return status, result
		}
	}

	//TODO: image verification here

	err = finishUpload(num)
	if err != nil {
		err := reportFailure(num)
		if err != nil {
		    log.Error("[ACI API]Internal Server Error: %v", err.Error())
			status, result := msgFailure("Internal Server Error", msg.Reason)
			return status, result
		}
	}

	succmsg := models.CompleteMsg{
		Success: true,
	}

	result, _ := json.Marshal(succmsg)
	return http.StatusInternalServerError, result
}

func reportFailure(num int) error {
	err := abortUpload(num)
	if err != nil {
		return err
	}
	return nil
}

func msgFailure(msg, clientmsg string) (int, []byte) {
	failmsg := models.CompleteMsg{
		Success:      false,
		Reason:       clientmsg,
		ServerReason: msg,
	}
	result, _ := json.Marshal(failmsg)
	return http.StatusInternalServerError, result
}

func abortUpload(num int) error {
	newuploadLock.Lock()
	delete(uploads, num)
	newuploadLock.Unlock()

	tmpaci := path.Join(directory, "tmp", strconv.Itoa(num))
	_, err := os.Stat(tmpaci)
	if err == nil {
		err = os.Remove(tmpaci)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	tmpsig := path.Join(directory, "tmp", strconv.Itoa(num)+".asc")
	_, err = os.Stat(tmpsig)
	if err == nil {
		err = os.Remove(tmpsig)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	return nil
}

func finishUpload(num int) error {
	newuploadLock.Lock()
	up, ok := uploads[num]
	if ok {
		delete(uploads, num)
	}
	newuploadLock.Unlock()
	if !ok {
		return fmt.Errorf("no such upload: %d", num)
	}

	err := os.Rename(path.Join(directory, "tmp", strconv.Itoa(num)),
		path.Join(directory, up.Image))
	if err != nil {
		return err
	}

	err = os.Rename(path.Join(directory, "tmp", strconv.Itoa(num)+".asc"),
		path.Join(directory, up.Image+".asc"))
	if err != nil {
		return err
	}

	return nil
}

func newUpload(image string) int {
	newuploadLock.Lock()
	uploadcounter++
	uploads[uploadcounter] = &models.Upload{
		Started: time.Now(),
		Image:   image,
	}
	newuploadLock.Unlock()
	return uploadcounter
}

func getUpload(num int) *models.Upload {
	var up *models.Upload
	newuploadLock.Lock()
	up, ok := uploads[num]
	newuploadLock.Unlock()
	if !ok {
		return nil
	}
	return up
}

func gotSig(num int) error {
	newuploadLock.Lock()
	_, ok := uploads[num]
	if ok {
		uploads[num].GotSig = true
	}
	newuploadLock.Unlock()
	if !ok {
		return fmt.Errorf("no such upload: %d", num)
	}
	return nil
}

func gotACI(num int) error {
	newuploadLock.Lock()
	_, ok := uploads[num]
	if ok {
		uploads[num].GotACI = true
	}
	newuploadLock.Unlock()
	if !ok {
		return fmt.Errorf("no such upload: %d", num)
	}
	return nil
}

func gotMan(num int) error {
	newuploadLock.Lock()
	_, ok := uploads[num]
	if ok {
		uploads[num].GotMan = true
	}
	newuploadLock.Unlock()
	if !ok {
		return fmt.Errorf("no such upload: %d", num)
	}
	return nil
}

func listACIs() ([]models.Aci, error) {
	files, err := ioutil.ReadDir(directory)
	if err != nil {
		return nil, err
	}

	var acis []models.Aci
	for _, file := range files {
		_, fname := path.Split(file.Name())
		tokens := strings.Split(fname, "-")
		if len(tokens) != 4 {
			continue
		}

		tokens1 := strings.Split(tokens[3], ".")
		if len(tokens1) != 2 {
			continue
		}

		if tokens1[1] != "aci" {
			continue
		}

		var signed bool

		_, err := os.Stat(path.Join(directory, fname+".asc"))
		if err == nil {
			signed = true
		} else if os.IsNotExist(err) {
			signed = false
		} else {
			return nil, err
		}

		details := models.Acidetails{
			Version: tokens[1],
			OS:      tokens[2],
			Arch:    tokens1[0],
			Signed:  signed,
			LastMod: file.ModTime().Format("Mon Jan 2 15:04:05 -0700 MST 2006"),
		}

		// If the last ACI added to the list has the same name
		if len(acis) > 0 && acis[len(acis)-1].Name == tokens[0] {
			acis[len(acis)-1].Details = append(acis[len(acis)-1].Details,
				details)
		} else {
			acis = append(acis, models.Aci{
				Name:    tokens[0],
				Details: []models.Acidetails{details},
			})
		}
	}

	return acis, nil
}
