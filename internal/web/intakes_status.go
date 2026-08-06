package web

// intakesNavDataKey es la clave con la que una página declara que el enlace a la bandeja debe salir
// en la barra superior. Solo la ponen las páginas que resolvieron las features del tenant; en el
// resto la clave no existe, la plantilla la lee como falsa y el enlace no se emite.
const intakesNavDataKey = "IntakesNav"

// intakeStatusOption es una clave del ciclo de vida con su nombre de negocio.
type intakeStatusOption struct {
	Key   string
	Label string
}

// intakeStatusOptions es un diccionario de PRESENTACIÓN: traduce las claves del ciclo de vida
// (D-041.10, en inglés por INV-09) al nombre con el que el dueño del negocio las llama. Alimenta el
// filtro de la bandeja y la etiqueta de cada fila.
//
// Que quede claro qué NO es, porque la distinción es la que sostiene esta pantalla: aquí no hay
// máquina de estados. No se dice desde dónde se llega a cada estado ni adónde se puede ir — eso lo
// decide `internal/intakes/status.go` de la plataforma y viaja por la API. Esto es una tabla de
// nombres, y su peor fallo posible es cosmético: si la Ola 4 añade un estado nuevo, `intakeStatusLabel`
// devuelve la clave cruda y el filtro no lo lista, pero nada opera mal ni se ofrece una transición
// falsa.
//
// El orden es el del ciclo de vida (no alfabético): el desplegable de filtro se lee como el recorrido
// que hace un pedido de verdad.
var intakeStatusOptions = []intakeStatusOption{
	{Key: "open", Label: "abierto"},
	{Key: "pending_approval", Label: "por aprobar"},
	{Key: "needs_info", Label: "falta info"},
	{Key: "confirmed", Label: "confirmado"},
	{Key: "deposit_requested", Label: "seña solicitada"},
	{Key: "deposit_paid", Label: "señado"},
	{Key: "settled", Label: "saldado"},
	{Key: "rejected", Label: "rechazado"},
	{Key: "cancelled", Label: "cancelado"},
	{Key: "abandoned", Label: "abandonado"},
	{Key: "expired", Label: "vencido (histórico)"},
}

// intakeStatusLabels indexa el diccionario por clave (se arma una vez, al cargar el paquete).
var intakeStatusLabels = func() map[string]string {
	labels := make(map[string]string, len(intakeStatusOptions))
	for _, opt := range intakeStatusOptions {
		labels[opt.Key] = opt.Label
	}
	// `closed` es la clave con la que el módulo cart cierra desde el Plan 016 y que sigue viva en BD.
	// La API la normaliza a `confirmed` al leer, así que en teoría no llega hasta aquí; se traduce
	// igualmente para que una fila legada que se colara no se vea como un estado desconocido.
	labels["closed"] = "confirmado"
	return labels
}()

// intakeStatusLabel devuelve el nombre de negocio de un estado. Una clave que no esté en el
// diccionario se devuelve TAL CUAL: es preferible que el operador vea `deposit_refunded` a que la
// pantalla se invente una traducción o esconda un estado que existe.
func intakeStatusLabel(status string) string {
	if label, ok := intakeStatusLabels[status]; ok {
		return label
	}
	return status
}

// labelsOf traduce una lista de claves (p. ej. los destinos permitidos de un 422).
func labelsOf(statuses []string) []string {
	out := make([]string, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, intakeStatusLabel(s))
	}
	return out
}
