package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
	"github.com/gorilla/mux"
	qrcode "github.com/skip2/go-qrcode"
	libDatabox "github.com/toshbrown/lib-go-databox"
)

var dboxproxy *DataboxProxyMiddleware

func ServeSecure(cm *ContainerManager, password string) {

	//pull required databox components from the ContainerManager
	cli := cm.cli
	ac := cm.ArbiterClient
	request := cm.Request

	//start the https server for the app UI
	r := mux.NewRouter()

	dboxproxy = NewProxyMiddleware("/certs/containerManager.crt")
	//proxy to the arbiter ui
	dboxproxy.Add("arbiter")

	dboxauth := NewAuthMiddleware(password, dboxproxy)

	libDatabox.Debug("Installing databox Middlewares")
	r.Use(dboxauth.AuthMiddleware, dboxproxy.ProxyMiddleware)

	r.HandleFunc("/api/cmlogs", func(w http.ResponseWriter, r *http.Request) {
		jsonString := cm.Logger.GetLastNLogEntriesRaw(100)
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		w.Write(jsonString)
	}).Methods("GET")

	r.HandleFunc("/api/qrcode.png", func(w http.ResponseWriter, r *http.Request) {

		type qrData struct {
			IP         string   `json:"ip"`
			IPs        []string `json:"ips"`
			IPExternal string   `json:"ipExternal"`
			Hostname   string   `json:hostname`
			Token      string   `json:"token"`
		}

		data := qrData{
			IP:         cm.Options.InternalIPs[0],
			IPs:        cm.Options.InternalIPs, //TODO tell kev about this
			IPExternal: cm.Options.ExternalIP,
			Hostname:   cm.Options.Hostname,
			Token:      "Token=" + password,
		}

		json, err := json.Marshal(data)
		if err != nil {
			libDatabox.Err("[/api/qrcode.png] Error parsing JSON " + err.Error())
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"msg":` + err.Error() + `}`))
			return
		}
		var png []byte
		png, err = qrcode.Encode(string(json), qrcode.Medium, 256)
		if err != nil {
			libDatabox.Err("[/api/qrcode.png] Error making  qrcode" + err.Error())
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"msg":` + err.Error() + `}`))
			return
		}

		w.Header().Set("Content-Type", "application/png")
		w.WriteHeader(http.StatusOK)
		w.Write(png)

	})

	r.HandleFunc("/api/datasource/list", func(w http.ResponseWriter, r *http.Request) {
		libDatabox.Debug("/api/datasource/list called")
		hyperCatRoot, err := ac.GetRootDataSourceCatalogue()
		if err != nil {
			libDatabox.Err("/api/datasource/list GetRootDataSourceCatalogue " + err.Error())
		}

		//hcr, _ := json.Marshal(hyperCatRoot)
		//libDatabox.Debug("/api/datasource/list hyperCatRoot=" + string(hcr))
		var datasources []libDatabox.HypercatItem
		for _, item := range hyperCatRoot.Items {
			//get the store cat
			storeURL, _ := libDatabox.GetStoreURLFromDsHref(item.Href)
			sc := libDatabox.NewCoreStoreClient(request, ac, "/run/secrets/ZMQ_PUBLIC_KEY", storeURL, false)
			storeCat, err := sc.GetStoreDataSourceCatalogue(item.Href)
			if err != nil {
				libDatabox.Err("[/api/datasource/list] Error GetStoreDataSourceCatalogue " + err.Error())
			}
			//src, _ := json.Marshal(storeCat)
			//libDatabox.Debug("/api/datasource/list got store cat: " + string(src))
			//build the datasource list
			for _, ds := range storeCat.Items {
				libDatabox.Debug("/api/datasource/list " + ds.Href)
				datasources = append(datasources, ds)
			}
		}
		jsonString, err := json.Marshal(datasources)
		if err != nil {
			libDatabox.Err("[/api/datasource/list] Error " + err.Error())
		}
		libDatabox.Debug("[/api/datasource/list] sending cat to client: " + string(jsonString))
		w.Write(jsonString)

	}).Methods("GET")

	r.HandleFunc("/api/store/cat/{store}", func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		store := vars["store"]

		storeURL := "tcp://" + store + ":5555"
		storeHref := "https://" + store + ":8080"
		sc := libDatabox.NewCoreStoreClient(request, ac, "/run/secrets/ZMQ_PUBLIC_KEY", storeURL, false)
		storeCat, err := sc.GetStoreDataSourceCatalogue(storeHref)
		if err != nil {
			libDatabox.Err("[/api/store/cat/{store}] Error GetStoreDataSourceCatalogue " + err.Error())
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"msg":` + err.Error() + `}`))
			return
		}
		catStr, _ := json.Marshal(storeCat)

		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		w.Write(catStr)

	}).Methods("GET")

	r.HandleFunc("/api/installed/list", func(w http.ResponseWriter, r *http.Request) {

		filters := filters.NewArgs()
		//filters.Add("label", "databox.type")
		services, _ := cli.ServiceList(context.Background(), types.ServiceListOptions{Filters: filters})

		res := []string{}
		for _, service := range services {
			res = append(res, service.Spec.Name)
		}

		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(res); err != nil {
			libDatabox.Err("error encoding json " + err.Error())
		}

	}).Methods("GET")

	type listResult struct {
		Name         string          `json:"name"`
		Type         string          `json:"type"`
		DesiredState swarm.TaskState `json:"desiredState"`
		State        swarm.TaskState `json:"state"`
		Status       swarm.TaskState `json:"status"`
	}

	r.HandleFunc("/api/{type}/list", func(w http.ResponseWriter, r *http.Request) {

		vars := mux.Vars(r)
		serviceType := vars["type"]

		services, _ := cli.ServiceList(context.Background(), types.ServiceListOptions{})

		res := []listResult{}
		for _, service := range services {

			val, exists := service.Spec.Labels["databox.type"]
			if exists == false {
				//its not a databox service
				continue
			}
			if val != serviceType {
				//this is not the service were looking for
				continue
			}
			lr := listResult{
				Name: service.Spec.Name,
				Type: serviceType,
			}

			taskFilters := filters.NewArgs()
			taskFilters.Add("service", service.Spec.Name)
			tasks, _ := cli.TaskList(context.Background(), types.TaskListOptions{
				Filters: taskFilters,
			})
			if len(tasks) > 0 {
				latestTasks := tasks[0]
				latestTime := latestTasks.UpdatedAt

				for _, t := range tasks {
					if t.UpdatedAt.After(latestTime) {
						latestTasks = t
						latestTime = latestTasks.UpdatedAt
					}
				}

				lr.DesiredState = latestTasks.DesiredState
				lr.State = latestTasks.Status.State
				lr.Status = latestTasks.Status.State
			}

			res = append(res, lr)
		}

		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(res); err != nil {
			libDatabox.Err("[/api/{type}/list] error encoding json " + err.Error())
		}

	}).Methods("GET")

	r.HandleFunc("/api/install", func(w http.ResponseWriter, r *http.Request) {

		defer r.Body.Close()
		slaString, _ := ioutil.ReadAll(r.Body)
		sla := libDatabox.SLA{}
		err := json.Unmarshal(slaString, &sla)
		if err != nil {
			libDatabox.Err("[/api/install] Error invalid sla json " + err.Error() + "JSON=" + string(slaString))
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"msg":` + err.Error() + `}`))
			return
		}

		libDatabox.Info("[/api/install] installing " + sla.Name)

		//add to proxy
		libDatabox.Debug("/api/install dboxproxy.Add " + sla.Name)
		dboxproxy.Add(sla.Name)

		//TODO check and return an error!!!
		libDatabox.Debug("/api/install LaunchFromSLA")
		cm.LaunchFromSLA(sla)

		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":200,"msg":"Success"}`))

		libDatabox.Debug("/api/install finished")

	}).Methods("POST")

	r.HandleFunc("/api/restart", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")

		bodyString, err := ioutil.ReadAll(r.Body)
		type jsonStruct struct {
			Name string `json:"id"`
		}
		jsonBody := jsonStruct{}
		err = json.Unmarshal(bodyString, &jsonBody)
		if err != nil {
			libDatabox.Err("[/api/restart] Malformed JSON " + err.Error())
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"msg": "Malformed JSON"}`))
			return
		}
		if jsonBody.Name == "" {
			libDatabox.Err("[/api/restart] Missing container name ")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"msg":Missing container name"}`))
			return
		}
		libDatabox.Info("[/api/restart] restarting - " + jsonBody.Name)

		err = cm.Restart(jsonBody.Name)
		if err != nil {
			libDatabox.Err("[/api/restart] Restrat " + jsonBody.Name + " failed. " + err.Error())
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"msg":"` + err.Error() + `"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":200,"msg":"Success"}`))
		return
	}).Methods("POST")

	r.HandleFunc("/api/uninstall", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		bodyString, err := ioutil.ReadAll(r.Body)
		type jsonStruct struct {
			Name string `json:"id"`
		}
		jsonBody := jsonStruct{}
		err = json.Unmarshal(bodyString, &jsonBody)
		if err != nil {
			libDatabox.Err("[/api/restart] Malformed JSON " + err.Error())
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"msg":"Malformed JSON"}`))
			return
		}
		if jsonBody.Name == "" {
			libDatabox.Err("[/api/uninstall] Missing container name (id)")
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"msg":Missing container name"}`))
			return
		}
		libDatabox.Info("[/api/uninstall] uninstalling " + jsonBody.Name)

		dboxproxy.Del(jsonBody.Name)

		err = cm.Uninstall(jsonBody.Name)
		if err != nil {
			libDatabox.Err("[/api/uninstall] Uninstall of " + jsonBody.Name + " failed. " + err.Error())
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"msg":"` + err.Error() + `"}`))
			return
		}

		libDatabox.Debug("/api/uninstall finished")

		w.Write([]byte(`{"status":200,"msg":"Success"}`))

	}).Methods("POST")

	static := http.FileServer(http.Dir("./www/https"))
	r.PathPrefix("/").Handler(static)

	libDatabox.ChkErrFatal(http.ListenAndServeTLS(":443", "./certs/container-manager.pem", "./certs/container-manager.pem", r))
}
