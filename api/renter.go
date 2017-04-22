package api

// TODO: When setting renter settings, leave empty values unchanged instead of
// zeroing them out.

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/NebulousLabs/Sia/build"
	"github.com/NebulousLabs/Sia/modules"
	"github.com/NebulousLabs/Sia/modules/renter"
	"github.com/NebulousLabs/Sia/types"

	"github.com/julienschmidt/httprouter"
	"strconv"
	"time"
)

var (
	// recommendedHosts is the number of hosts that the renter will form
	// contracts with if the value is not specified explicitly in the call to
	// SetSettings.
	recommendedHosts = build.Select(build.Var{
		Standard: uint64(50),
		Dev:      uint64(2),
		Testing:  uint64(1),
	}).(uint64)

	// requiredHosts specifies the minimum number of hosts that must be set in
	// the renter settings for the renter settings to be valid. This minimum is
	// there to prevent users from shooting themselves in the foot.
	requiredHosts = build.Select(build.Var{
		Standard: uint64(20),
		Dev:      uint64(1),
		Testing:  uint64(1),
	}).(uint64)

	// requiredParityPieces specifies the minimum number of parity pieces that
	// must be used when uploading a file. This minimum exists to prevent users
	// from shooting themselves in the foot.
	requiredParityPieces = build.Select(build.Var{
		Standard: int(12),
		Dev:      int(0),
		Testing:  int(0),
	}).(int)

	// requiredRedundancy specifies the minimum redundancy that will be
	// accepted by the renter when uploading a file. This minimum exists to
	// prevent users from shooting themselves in the foot.
	requiredRedundancy = build.Select(build.Var{
		Standard: float64(2),
		Dev:      float64(1),
		Testing:  float64(1),
	}).(float64)

	// requiredRenewWindow establishes the minimum allowed renew window for the
	// renter settings. This minimum is here to prevent users from shooting
	// themselves in the foot.
	requiredRenewWindow = build.Select(build.Var{
		Standard: types.BlockHeight(288),
		Dev:      types.BlockHeight(1),
		Testing:  types.BlockHeight(1),
	}).(types.BlockHeight)
)

type (
	// RenterGET contains various renter metrics.
	RenterGET struct {
		Settings         modules.RenterSettings `json:"settings"`
		FinancialMetrics RenterFinancialMetrics `json:"financialmetrics"`
		CurrentPeriod    types.BlockHeight      `json:"currentperiod"`
	}

	// RenterFinancialMetrics contains metrics about how much the Renter has
	// spent on storage, uploads, and downloads.
	RenterFinancialMetrics struct {
		// Amount of money in the allowance spent on file contracts including
		// fees.
		ContractSpending types.Currency `json:"contractspending"`

		DownloadSpending types.Currency `json:"downloadspending"`
		StorageSpending  types.Currency `json:"storagespending"`
		UploadSpending   types.Currency `json:"uploadspending"`

		// Amount of money in the allowance that has not been spent.
		Unspent types.Currency `json:"unspent"`
	}

	// RenterContract represents a contract formed by the renter.
	RenterContract struct {
		// Amount of contract funds that have been spent on downloads.
		DownloadSpending types.Currency `json:"downloadspending"`
		// Block height that the file contract ends on.
		EndHeight types.BlockHeight `json:"endheight"`
		// Fees paid in order to form the file contract.
		Fees types.Currency `json:"fees"`
		// Public key of the host the contract was formed with.
		HostPublicKey types.SiaPublicKey `json:"hostpublickey"`
		// ID of the file contract.
		ID types.FileContractID `json:"id"`
		// A signed transaction containing the most recent contract revision.
		LastTransaction types.Transaction `json:"lasttransaction"`
		// Address of the host the file contract was formed with.
		NetAddress modules.NetAddress `json:"netaddress"`
		// Remaining funds left for the renter to spend on uploads & downloads.
		RenterFunds types.Currency `json:"renterfunds"`
		// Size of the file contract, which is typically equal to the number of
		// bytes that have been uploaded to the host.
		Size uint64 `json:"size"`
		// Block height that the file contract began on.
		StartHeight types.BlockHeight `json:"startheight"`
		// Amount of contract funds that have been spent on storage.
		StorageSpending types.Currency `json:"StorageSpending"`
		// Total cost to the wallet of forming the file contract.
		TotalCost types.Currency `json:"totalcost"`
		// Amount of contract funds that have been spent on uploads.
		UploadSpending types.Currency `json:"uploadspending"`
	}

	// RenterContracts contains the renter's contracts.
	RenterContracts struct {
		Contracts []RenterContract `json:"contracts"`
	}

	// DownloadQueue contains the renter's download queue.
	RenterDownloadQueue struct {
		Downloads []DownloadInfo `json:"downloads"`
	}

	// RenterFiles lists the files known to the renter.
	RenterFiles struct {
		Files []modules.FileInfo `json:"files"`
	}

	// RenterLoad lists files that were loaded into the renter.
	RenterLoad struct {
		FilesAdded []string `json:"filesadded"`
	}

	// RenterPricesGET lists the data that is returned when a GET call is made
	// to /renter/prices.
	RenterPricesGET struct {
		modules.RenterPriceEstimation
	}

	// RenterShareASCII contains an ASCII-encoded .sia file.
	RenterShareASCII struct {
		ASCIIsia string `json:"asciisia"`
	}

	// DownloadInfo contains all client-facing information of a file.
	DownloadInfo struct {
		SiaPath     string    `json:"siapath"`
		Destination string    `json:"destination"`
		Filesize    uint64    `json:"filesize"`
		Received    uint64    `json:"received"`
		StartTime   time.Time `json:"starttime"`
		Error       string    `json:"error"`
	}
)

// renterHandlerGET handles the API call to /renter.
func (api *API) renterHandlerGET(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	settings := api.renter.Settings()
	periodStart := api.renter.CurrentPeriod()
	// calculate financial metrics from contracts. We use the special
	// AllContracts method to include contracts that are offline.
	var fm RenterFinancialMetrics
	fm.Unspent = settings.Allowance.Funds
	contracts := api.renter.(interface {
		AllContracts() []modules.RenterContract
	}).AllContracts()
	for _, c := range contracts {
		if c.StartHeight < periodStart {
			continue
		}
		fm.ContractSpending = fm.ContractSpending.Add(c.TotalCost)
		fm.DownloadSpending = fm.DownloadSpending.Add(c.DownloadSpending)
		fm.UploadSpending = fm.UploadSpending.Add(c.UploadSpending)
		fm.StorageSpending = fm.StorageSpending.Add(c.StorageSpending)
		// total unspent is:
		//    allowance - (cost to form contracts) + (money left in contracts)
		if fm.Unspent.Add(c.RenterFunds()).Cmp(c.TotalCost) > 0 {
			fm.Unspent = fm.Unspent.Add(c.RenterFunds()).Sub(c.TotalCost)
		}
	}

	WriteJSON(w, RenterGET{
		Settings:         settings,
		FinancialMetrics: fm,
		CurrentPeriod:    periodStart,
	})
}

// renterHandlerPOST handles the API call to set the Renter's settings.
func (api *API) renterHandlerPOST(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Scan the allowance amount.
	funds, ok := scanAmount(req.FormValue("funds"))
	if !ok {
		WriteError(w, Error{"unable to parse funds"}, http.StatusBadRequest)
		return
	}

	// Scan the number of hosts to use. (optional parameter)
	var hosts uint64
	if req.FormValue("hosts") != "" {
		_, err := fmt.Sscan(req.FormValue("hosts"), &hosts)
		if err != nil {
			WriteError(w, Error{"unable to parse hosts: " + err.Error()}, http.StatusBadRequest)
			return
		}
		if hosts != 0 && hosts < requiredHosts {
			WriteError(w, Error{fmt.Sprintf("insufficient number of hosts, need at least %v but have %v", recommendedHosts, hosts)}, http.StatusBadRequest)
			return
		}
	} else {
		hosts = recommendedHosts
	}

	// Scan the period.
	var period types.BlockHeight
	_, err := fmt.Sscan(req.FormValue("period"), &period)
	if err != nil {
		WriteError(w, Error{"unable to parse period: " + err.Error()}, http.StatusBadRequest)
		return
	}

	// Scan the renew window. (optional parameter)
	var renewWindow types.BlockHeight
	if req.FormValue("renewwindow") != "" {
		_, err = fmt.Sscan(req.FormValue("renewwindow"), &renewWindow)
		if err != nil {
			WriteError(w, Error{"unable to parse renewwindow: " + err.Error()}, http.StatusBadRequest)
			return
		}
		if renewWindow != 0 && renewWindow < requiredRenewWindow {
			WriteError(w, Error{fmt.Sprintf("renew window is too small, must be at least %v blocks but have %v blocks", requiredRenewWindow, renewWindow)}, http.StatusBadRequest)
			return
		}
	} else {
		renewWindow = period / 2
	}

	// Set the settings in the renter.
	err = api.renter.SetSettings(modules.RenterSettings{
		Allowance: modules.Allowance{
			Funds:       funds,
			Hosts:       hosts,
			Period:      period,
			RenewWindow: renewWindow,
		},
	})
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}
	WriteSuccess(w)
}

// renterContractsHandler handles the API call to request the Renter's contracts.
func (api *API) renterContractsHandler(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	contracts := []RenterContract{}
	for _, c := range api.renter.Contracts() {
		contracts = append(contracts, RenterContract{
			DownloadSpending: c.DownloadSpending,
			EndHeight:        c.EndHeight(),
			Fees:             c.TxnFee.Add(c.SiafundFee).Add(c.ContractFee),
			HostPublicKey:    c.HostPublicKey,
			ID:               c.ID,
			LastTransaction:  c.LastRevisionTxn,
			NetAddress:       c.NetAddress,
			RenterFunds:      c.RenterFunds(),
			Size:             c.LastRevision.NewFileSize,
			StartHeight:      c.StartHeight,
			StorageSpending:  c.StorageSpending,
			TotalCost:        c.TotalCost,
			UploadSpending:   c.UploadSpending,
		})
	}
	WriteJSON(w, RenterContracts{
		Contracts: contracts,
	})
}

// renterDownloadsHandler handles the API call to request the download queue.
func (api *API) renterDownloadsHandler(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	dlq := api.renter.DownloadQueue()

	// Translate []modules.DownloadInfo to []api.DownloadInfo.
	downloads := make([]DownloadInfo, len(dlq))
	for i := range dlq {
		d := dlq[len(dlq)-i-1]

		// Find the destination of the download.
		dstwriter, ok := (d.Destination).(*modules.DownloadFileWriter)
		var dst string
		if ok {
			dst = dstwriter.Location
		} else {
			dst = "httpresp"
		}

		downloads[i] = DownloadInfo{
			SiaPath:     d.SiaPath,
			Destination: dst,
			Filesize:    d.Filesize,
			StartTime:   d.StartTime,
			Received:    d.Received,
			Error:       d.Error,
		}
	}
	WriteJSON(w, RenterDownloadQueue{
		Downloads: downloads,
	})
}

// renterLoadHandler handles the API call to load a '.sia' file.
func (api *API) renterLoadHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	source := req.FormValue("source")
	if !filepath.IsAbs(source) {
		WriteError(w, Error{"source must be an absolute path"}, http.StatusBadRequest)
		return
	}

	files, err := api.renter.LoadSharedFiles(source)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	WriteJSON(w, RenterLoad{FilesAdded: files})
}

// renterLoadAsciiHandler handles the API call to load a '.sia' file
// in ASCII form.
func (api *API) renterLoadAsciiHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	files, err := api.renter.LoadSharedFilesAscii(req.FormValue("asciisia"))
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	WriteJSON(w, RenterLoad{FilesAdded: files})
}

// renterRenameHandler handles the API call to rename a file entry in the
// renter.
func (api *API) renterRenameHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	err := api.renter.RenameFile(strings.TrimPrefix(ps.ByName("siapath"), "/"), req.FormValue("newsiapath"))
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	WriteSuccess(w)
}

// renterFilesHandler handles the API call to list all of the files.
func (api *API) renterFilesHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	WriteJSON(w, RenterFiles{
		Files: api.renter.FileList(),
	})
}

// renterPricesHandler reports the expected costs of various actions given the
// renter settings and the set of available hosts.
func (api *API) renterPricesHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	WriteJSON(w, RenterPricesGET{
		RenterPriceEstimation: api.renter.PriceEstimation(),
	})
}

// renterDeleteHandler handles the API call to delete a file entry from the
// renter.
func (api *API) renterDeleteHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	err := api.renter.DeleteFile(strings.TrimPrefix(ps.ByName("siapath"), "/"))
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	WriteSuccess(w)
}

// renterDownloadHandler handles the API call to download a file.
func (api *API) renterDownloadHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	p, errmsg := api.parseAndValidateDownloadParameters(w, req, ps)
	if errmsg != nil {
		WriteError(w, *errmsg, http.StatusBadRequest)
		return
	}

	if p.Async { // Create goroutine if `async` param set.
		go api.renter.DownloadSection(p)
	} else {
		err := api.renter.DownloadSection(p)
		if err != nil {
			WriteError(w, Error{"download failed: " + err.Error()}, http.StatusInternalServerError)
			return
		}
	}

	WriteSuccess(w)
}

// renterDownloadAsyncHandler handles the API call to download a file asynchronously.
func (api *API) renterDownloadAsyncHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	p, errmsg := api.parseAndValidateDownloadParameters(w, req, ps)
	if errmsg != nil {
		WriteError(w, *errmsg, http.StatusBadRequest)
	}

	// Set async param to true.
	// This parameter may be used in the future so it is set for safety reasons,
	// its intended use is to determine whether to call DownloadSection in a goroutine.
	p.Async = true

	go api.renter.DownloadSection(p)

	WriteSuccess(w)
}

func (api *API) parseAndValidateDownloadParameters(w http.ResponseWriter, req *http.Request, ps httprouter.Params) (*modules.RenterDownloadParameters, *Error) {
	destination := req.FormValue("destination")

	// The offset and length in bytes.
	offsetparam := req.FormValue("offset")
	lengthparam := req.FormValue("length")

	// Determines whether the response is written to response body.
	httprespparam := req.FormValue("httpresp")

	// Determines whether to return on completion of download or straight away.
	// If httprespparam is present, this parameter is ignored.
	asyncparam := req.FormValue("async")

	// Parse the offset and length parameters. TODO(rnabel): Handle empty string.
	var offset, length uint64
	var err error
	if len(offsetparam) > 0 {
		offset, err = strconv.ParseUint(offsetparam, 10, 64)
		if err != nil {
			return nil, &Error{"could not decode the offset as uint64: " +
				err.Error()}
		}
	}
	if len(lengthparam) > 0 {
		length, err = strconv.ParseUint(lengthparam, 10, 64)
		if err != nil {
			return nil, &Error{"could not decode the length as uint64: " +
				err.Error()}
		}

	}
	// Verify that if either offset or length have been provided that both were provided.
	offparampassed := len(offsetparam) > 0
	lenparampassed := len(lengthparam) > 0
	if (offparampassed || lenparampassed) &&
		!(offparampassed && lenparampassed) {
		var missingfield = "offset"
		if lenparampassed {
			missingfield = "length"
		}
		return nil, &Error{"either both \"offset\" and " +
			"\"length\" have to be specified or neither. " +
			missingfield + " has not been specified."}
	}

	// Parse the httpresp parameter.
	httpresp, errmsg := stringToBool(httprespparam)
	if errmsg != nil {
		return nil, errmsg
	}

	// Parse the async parameter.
	async, errmsg := stringToBool(asyncparam)
	if errmsg != nil {
		return nil, errmsg
	}

	siapath := strings.TrimPrefix(ps.ByName("siapath"), "/") // Sia file name.

	// Lookup the file associated with the nickname.
	file, exists := api.renter.GetFile(siapath)
	if !exists {
		return nil, &Error{Message: "download failed: no file with that path"}
	}

	if !offparampassed { // Determine if entire file is to be downloaded.
		offset = 0
		length = file.Filesize
	}

	// Instantiate the correct DownloadWriter implementation
	// (e.g. content written to file or response body).
	var dw modules.DownloadWriter
	if httpresp {
		dw = modules.NewDownloadHttpWriter(w, offset, length)
	} else {
		// Ensure that destination is valid beforehands.
		// Check that the destination path is absolute.
		if !filepath.IsAbs(destination) {
			return nil, &Error{"destination must be an absolute path"}
		}
		dw = modules.NewDownloadFileWriter(destination, offset, length)
	}

	return &modules.RenterDownloadParameters{
		Async:    async,
		DlWriter: dw,
		Httpresp: httpresp,
		Length:   length,
		Offset:   offset,
		Siapath:  siapath,
	}, nil
}

// renterShareHandler handles the API call to create a '.sia' file that
// shares a set of file.
func (api *API) renterShareHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	destination := req.FormValue("destination")
	// Check that the destination path is absolute.
	if !filepath.IsAbs(destination) {
		WriteError(w, Error{"destination must be an absolute path"}, http.StatusBadRequest)
		return
	}

	err := api.renter.ShareFiles(strings.Split(req.FormValue("siapaths"), ","), destination)
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}

	WriteSuccess(w)
}

// renterShareAsciiHandler handles the API call to return a '.sia' file
// in ascii form.
func (api *API) renterShareAsciiHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	ascii, err := api.renter.ShareFilesAscii(strings.Split(req.FormValue("siapaths"), ","))
	if err != nil {
		WriteError(w, Error{err.Error()}, http.StatusBadRequest)
		return
	}
	WriteJSON(w, RenterShareASCII{
		ASCIIsia: ascii,
	})
}

// renterUploadHandler handles the API call to upload a file.
func (api *API) renterUploadHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	source := req.FormValue("source")
	if !filepath.IsAbs(source) {
		WriteError(w, Error{"source must be an absolute path"}, http.StatusBadRequest)
		return
	}

	// Check whether the erasure coding parameters have been supplied.
	var ec modules.ErasureCoder
	if req.FormValue("datapieces") != "" || req.FormValue("paritypieces") != "" {
		// Check that both values have been supplied.
		if req.FormValue("datapieces") == "" || req.FormValue("paritypieces") == "" {
			WriteError(w, Error{"must provide both the datapieces paramaeter and the paritypieces parameter if specifying erasure coding parameters"}, http.StatusBadRequest)
			return
		}

		// Parse the erasure coding parameters.
		var dataPieces, parityPieces int
		_, err := fmt.Sscan(req.FormValue("datapieces"), &dataPieces)
		if err != nil {
			WriteError(w, Error{"unable to read parameter 'datapieces': " + err.Error()}, http.StatusBadRequest)
			return
		}
		_, err = fmt.Sscan(req.FormValue("paritypieces"), &parityPieces)
		if err != nil {
			WriteError(w, Error{"unable to read parameter 'paritypieces': " + err.Error()}, http.StatusBadRequest)
			return
		}

		// Verify that sane values for parityPieces and redundancy are being
		// supplied.
		if parityPieces < requiredParityPieces {
			WriteError(w, Error{fmt.Sprintf("a minimum of %v parity pieces is required, but %v parity pieces requested", parityPieces, requiredParityPieces)}, http.StatusBadRequest)
			return
		}
		redundancy := float64(dataPieces+parityPieces) / float64(dataPieces)
		if float64(dataPieces+parityPieces)/float64(dataPieces) < requiredRedundancy {
			WriteError(w, Error{fmt.Sprintf("a redundancy of %.2f is required, but redundancy of %.2f supplied", redundancy, requiredRedundancy)}, http.StatusBadRequest)
			return
		}

		// Create the erasure coder.
		ec, err = renter.NewRSCode(dataPieces, parityPieces)
		if err != nil {
			WriteError(w, Error{"unable to encode file using the provided parameters: " + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Call the renter to upload the file.
	err := api.renter.Upload(modules.FileUploadParams{
		Source:      source,
		SiaPath:     strings.TrimPrefix(ps.ByName("siapath"), "/"),
		ErasureCode: ec,
	})
	if err != nil {
		WriteError(w, Error{"upload failed: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	WriteSuccess(w)
}

// stringToBool converts "true" and "false" strings to their respective boolean value and returns an error if conversion is not possible.
func stringToBool(param string) (bool, *Error) {
	// Parse the async parameter.
	var out bool
	switch {
	case param == "true":
		out = true
	case len(param) == 0 || param == "false":
		out = false
	default:
		return false, &Error{"asyncparam has to be empty, \"true\" or \"false\""}
	}

	return out, nil
}
