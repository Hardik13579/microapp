package controllers

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/gorilla/mux"
	"github.com/islax/microapp"
	microappCtx "github.com/islax/microapp/context"
	microappError "github.com/islax/microapp/error"
	microappLog "github.com/islax/microapp/log"
	microappRepo "github.com/islax/microapp/repository"
	microappSecurity "github.com/islax/microapp/security"
	microappWeb "github.com/islax/microapp/web"
	tenantService "github.com/microapp/service"
	tenantModel "github.com/microapp/settingsmetadata/model"
	tenantsettingsRepo "github.com/microapp/settingsmetadata/repository"
	uuid "github.com/satori/go.uuid"
)

// NewPolicyProfileController creates a new policy profile controller
func NewSettingsMetadataController(app *microapp.App, repository microappRepo.Repository, tenantRepo tenantsettingsRepo.TenantSettingsRepository) *SettingsMetadataController {
	controller := &SettingsMetadataController{app: app, repository: tenantRepo}
	return controller

}

//SettingsMetadataController
type SettingsMetadataController struct {
	app        *microapp.App
	repository microappRepo.Repository
}

// RegisterRoutes implements interface RouteSpecifier
func (controller *SettingsMetadataController) RegisterRoutes(muxRouter *mux.Router) {
	apiRouter := muxRouter.PathPrefix("/api").Subrouter()
	policySettingsRouter := apiRouter.PathPrefix("/policyengine").Subrouter()

	policySettingsMetadataRouter := policySettingsRouter.PathPrefix("/settings-metadata").Subrouter()
	policySettingsMetadataRouter.HandleFunc("", microappSecurity.Protect(controller.app.Config, controller.getPolicySettingsMetadata, []string{"settingsmetadata:read"}, false)).Methods("GET")

	policyTenantSettingsRouter := policySettingsRouter.PathPrefix("/tenants/{id}").Subrouter()
	policyTenantSettingsRouter.HandleFunc("", microappSecurity.Protect(controller.app.Config, controller.get, []string{"tenantsettings:read"}, false)).Methods("GET")
	policyTenantSettingsRouter.HandleFunc("", microappSecurity.Protect(controller.app.Config, controller.update, []string{"tenantsettings:write"}, false)).Methods("PUT")
	policyTenantSettingsRouter.HandleFunc("/{settingName}", microappSecurity.Protect(controller.app.Config, controller.getByName, []string{"tenantsettings:read"}, false)).Methods("GET")

}

func (controller *SettingsMetadataController) getPolicySettingsMetadata(w http.ResponseWriter, r *http.Request, token *microappSecurity.JwtToken) {
	context := controller.app.NewExecutionContext(token, microapp.GetCorrelationIDFromRequest(r), "settingsmetadata.get", false, false)

	var settingsmetadata []map[string]interface{}
	jsonFile, err := os.Open(controller.app.Config.GetString("SETTINGS_METADATA_PATH"))
	if err != nil {
		context.LogError(err, fmt.Sprintf(microappLog.MessageGenericErrorTemplate, "opening settings-metadata config file."))
		microappWeb.RespondError(w, err)
		return
	}
	defer jsonFile.Close()
	byteValue, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		context.LogError(err, fmt.Sprintf(microappLog.MessageGenericErrorTemplate, "reading tenant role config file."))
		microappWeb.RespondError(w, err)
		return
	}
	json.Unmarshal(byteValue, &settingsmetadata)

	microappWeb.RespondJSON(w, http.StatusOK, settingsmetadata)
}

func (controller *SettingsMetadataController) get(w http.ResponseWriter, r *http.Request, token *microappSecurity.JwtToken) {
	context := controller.app.NewExecutionContext(token, microapp.GetCorrelationIDFromRequest(r), "tenantsettings.get", true, true)
	uow := context.GetUOW()
	defer uow.Complete()
	params := mux.Vars(r)
	stringTenantID := params["id"]

	tenantID, err := tenantService.GetTenantIDFromToken().GetTenantIDAsUUID(mux.Vars(r), token, stringTenantID)
	if err != nil {
		context.LogError(err, microappLog.MessageUnableToFindURLResource)
		microappWeb.RespondError(w, err)
		return
	}

	tenant, err := controller.getTenant(context, uow, controller.repository, tenantID)
	if err != nil {
		microappWeb.RespondError(w, err)
		return
	}

	microappWeb.RespondJSON(w, http.StatusOK, toDTO(tenant))
}

func (controller *SettingsMetadataController) update(w http.ResponseWriter, r *http.Request, token *microappSecurity.JwtToken) {
	context := controller.app.NewExecutionContext(token, microapp.GetCorrelationIDFromRequest(r), "tenantsettings.update", true, true)
	uow := context.GetUOW()
	defer uow.Complete()
	params := mux.Vars(r)
	stringTenantID := params["id"]

	var reqDTO tenantDTO
	if err := microappWeb.UnmarshalJSON(r, &reqDTO); err != nil {
		context.LogJSONParseError(err)
		microappWeb.RespondError(w, err)
		return
	}

	tenantID, err := tenantService.GetTenantIDFromToken().GetTenantIDAsUUID(mux.Vars(r), token, stringTenantID)
	if err != nil {
		context.LogError(err, microappLog.MessageUnableToFindURLResource)
		microappWeb.RespondError(w, err)
		return
	}

	tenant, err := controller.getTenant(context, uow, controller.repository, tenantID)
	if err != nil {
		microappWeb.RespondError(w, err)
		return
	}

	var settingsmetadata []tenantModel.SettingsMetaData
	jsonFile, err := os.Open(controller.app.Config.GetString("SETTINGS_METADATA_PATH"))
	if err != nil {
		context.LogError(err, fmt.Sprintf(microappLog.MessageGenericErrorTemplate, "opening settings-metadata config file."))
		return
	}
	defer jsonFile.Close()
	byteValue, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		context.LogError(err, fmt.Sprintf(microappLog.MessageGenericErrorTemplate, "reading tenant role config file."))
		return
	}
	json.Unmarshal(byteValue, &settingsmetadata)

	if err = tenant.Update(reqDTO.Settings, settingsmetadata); err != nil {
		context.LogError(err, microappLog.MessageNewEntityError)
		microappWeb.RespondError(w, err)
		return
	}

	err = controller.repository.Update(uow, &tenant)
	if err != nil {
		context.LogError(err, microappLog.MessageUpdateEntityError)
		microappWeb.RespondError(w, err)
		return
	}

	uow.Commit()
	responseDTO := toDTO(tenant)
	context.LoggerEventActionCompletion().Str("TenantId", responseDTO.ID.String()).Msg("Tenant settings updated")
	controller.app.DispatchEvent(token.Raw, "nil", "tenantsettings.updated", &responseDTO)
	microappWeb.RespondJSON(w, http.StatusOK, responseDTO)
}

func (controller *SettingsMetadataController) getByName(w http.ResponseWriter, r *http.Request, token *microappSecurity.JwtToken) {
	context := controller.app.NewExecutionContext(token, microapp.GetCorrelationIDFromRequest(r), "tenantsettings.get", true, true)
	uow := context.GetUOW()
	defer uow.Complete()

	params := mux.Vars(r)
	stringTenantID := params["id"]
	var settingsName interface{}
	tenantID, err := tenantService.GetTenantIDFromToken().GetTenantIDAsUUID(params, token, stringTenantID)
	if err != nil {
		context.LogError(err, microappLog.MessageUnableToFindURLResource)
		microappWeb.RespondError(w, err)
		return
	}

	tenant, err := controller.getTenant(context, uow, controller.repository, tenantID)
	if err != nil {
		microappWeb.RespondError(w, err)
		return
	}

	settings, err := tenant.GetSettings()
	if err != nil {
		context.LogError(err, microappLog.MessageGetEntityError)
		microappWeb.RespondError(w, err)
		return
	}

	for key, settingsValue := range settings {
		value := settingsValue.(map[string]interface{})
		requiredValue := value["value"]
		if params["settingName"] == key {
			settingsName = requiredValue
			break
		}
	}

	settingsParameter := map[string]interface{}{params["settingName"]: settingsName}
	microappWeb.RespondJSON(w, http.StatusOK, settingsParameter)
}

func (controller *SettingsMetadataController) getTenant(context microappCtx.ExecutionContext, uow *microappRepo.UnitOfWork, repository microappRepo.Repository, tenantID uuid.UUID) (*tenantModel.Tenant, error) {
	tenant := tenantModel.Tenant{}
	queryProcessor := []microappRepo.QueryProcessor{}
	queryProcessor = append(queryProcessor, microappRepo.Filter("id = ?", tenantID))
	if err := repository.GetFirst(uow, &tenant, queryProcessor); err != nil {
		if err.IsRecordNotFoundError() {
			context.LogError(err, microappLog.MessageUnableToFindURLResource)
			return nil, microappError.NewHTTPResourceNotFound("tenant", tenantID.String())
		}
		context.LogError(err, fmt.Sprintf(microappLog.MessageGenericErrorTemplate, "getting tenant from database"))
		return nil, err
	}
	return &tenant, nil
}

func toDTO(tenant *tenantModel.Tenant) tenantDTO {
	settings := make(map[string]interface{})
	json.Unmarshal([]byte(tenant.Settings), &settings)
	return tenantDTO{
		ID:       tenant.ID,
		Settings: settings,
	}
}

type tenantDTO struct {
	ID       uuid.UUID              `json:"id"`
	Settings map[string]interface{} `json:"settings"`
}