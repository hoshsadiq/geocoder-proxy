// Package all imports every provider package so their init() registrations
// run. The composition root imports this one package; adding a provider
// means a new package under provider/ plus one line here.
package all

import (
	_ "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/geoapify"
	_ "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/locationiq"
	_ "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/nominatim"
	_ "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/photon"
)
