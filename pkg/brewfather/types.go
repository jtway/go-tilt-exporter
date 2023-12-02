package brewfather

type Status string

const (
	Planning     Status = "Planning"
	Brewing      Status = "Brewing"
	Fermenting   Status = "Fermenting"
	Conditioning Status = "Conditioning"
	Completed    Status = "Completed"
	Archived     Status = "Archived"
)

type BatchShort struct {
	Id          string `json:"_id"`
	BatchNumber uint32 `json:"batchNo"`
	BrewDate    int64  `json:"brewDate"`
	Brewer      string `json:"brewer"`
	Name        string `json:"name"`
	Recipe      struct {
		Name string `json:"name"`
	} `json:"recipe"`
	Status Status `json:"status"`
}

type Fermentable struct {
	Attenuation   float32  `json:"attenuation"`
	Notes         string   `json:"notes"`
	Hidden        bool     `json:"hidden"`
	Color         int32    `json:"color"`
	GrainCategory string   `json:"grainCategory"`
	Origin        string   `json:"origin"`
	Inventory     *float32 `json:"inventory"`
	Type          *string  `json:"type"`
	Supplier      *string  `json:"supplier"`
	Protein       *string  `json:"protein"`
	Percentage    *float64 `json:"percentage"`
	Amount        float64  `json:"amount"`
	Name          string   `json:"name"`
	Id            string   `json:"_id"`
}

type Yeast struct {
	Amount         float32 `json:"amount"`
	Attenuation    float32 `json:"attenuation"`
	ProductId      string  `json:"productId"`
	MaxTemp        int32   `json:"maxTemp"`
	Description    string  `json:"description"`
	FermentsAll    bool    `json:"fermentsAll"`
	MaxAttenuation float32 `json:"maxAttenuation"`
	Type           string  `json:"type"`
	MinAttenuation float32 `json:"minAttenuation"`
	Flocculation   string  `json:"flocculation"`
	MinTemp        int32   `json:"minTempt"`
	Unit           string  `json:"unit"`
	Form           string  `json:"form"`
	Laboratory     string  `json:"laboratory"`
	Name           string  `json:"name"`
	Id             string  `json:"_id"`
	MaxAbv         float32 `json:"maxAbv"`
}

// @TODO Hops (Once Hops maybe pull recipe)

type TiltDevice struct {
	Hidden  bool   `json:"hidden"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	BatchId string `json:"batchId"`
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}
type TiltDevices struct {
	Mode    string       `json:"mode"`
	Temp    bool         `json:"temp"`
	Gravity bool         `json:"gravity"`
	Items   []TiltDevice `json:"items"`
	Enabled bool         `json:"enabled"`
}

type StreamDevices struct {
	Streams []Stream `json:"items"`
	Enabled bool     `json:"enabled"`
}

type Stream struct {
	Hidden  bool   `json:"hidden"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	BatchId string `json:"batchId"`
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
	LastLog uint64 `json:"lastLog"`
}

// Only Caring about Tilt Devices for the moment. The are many others.
type Devices struct {
	Streams StreamDevices `json:"stream"`
	Tilt    TiltDevices   `json:"tilt"`
}

type Batch struct {
	BatchNumber              uint32  `json:"batchNo"`
	FermentationStartDate    int64   `json:"fermentationStartDate"`
	Id                       string  `json:"_id"`
	EstimatedColor           float32 `json:"estimatedColor"`
	MeasuredKettleEfficiency float32 `json:"measuredKettleEfficiency"`
	EstimatedIbu             uint32  `json:"estimatedIbu"`
	Type                     string  `json:"type"`
	Name                     string  `json:"name"`
	MeasuredMashEfficiency   float32 `json:"measuredMashEfficiency"`
	Status                   Status  `json:"status"`
	Devices                  Devices `json:"devices"`
	MeasuredAttenuation      float32 `json:"measuredAttenuation"`
	EstimatedOg              float64 `json:"estimatedOg"`
	MeasuredAbv              float32 `json:"measuredAbv"`
	EstimatedTotalGravity    float64 `json:"estimatedTotalGravity"`
	EstimatedFg              float64 `json:"estimatedFg"`
	MeasuredEfficiency       float32 `json:"measuredEfficiency"`
	EstimatedBuGuRation      float32 `json:"estimatedBuGuRatio"`
	MeasuredOg               float32 `json:"measuredOg"`
	Brewer                   string  `json:"brewer"`
}
