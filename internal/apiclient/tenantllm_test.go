package apiclient

import (
	"reflect"
	"testing"
)

// TestTenantLLMNoTieneDondeGuardarLaCredencial es la INVARIANTE ESTRUCTURAL de T3.4, y existe porque
// el criterio de la tarea —«la clave nunca se re-pinta en el HTML»— no se sostiene hoy por disciplina
// sino por construcción: la plataforma no devuelve la credencial y este struct no tiene campo donde
// ponerla, así que ninguna plantilla puede pintarla aunque su autor quiera.
//
// 🔴 Esa garantía la borra UNA LÍNEA: un `APIKey string` añadido aquí «por comodidad» la deja lista
// para filtrarse, y ningún test de conducta lo vería —el fake tampoco la devuelve, así que el campo
// quedaría vacío y todo seguiría en verde—. Por eso lo que se vigila es la FORMA del tipo y no un
// comportamiento: se fija la lista exacta de campos, de modo que añadir uno obligue a venir aquí y
// decidirlo a propósito.
//
// Si algún día la API publica algo más (una huella, por ejemplo), el cambio es de una línea en esta
// lista — y esa línea es la conversación que este test existe para forzar.
func TestTenantLLMNoTieneDondeGuardarLaCredencial(t *testing.T) {
	quiero := []string{"Configured", "Via", "Provider", "Model", "KeySet", "ConsentedAt", "CreatedAt", "UpdatedAt"}

	typ := reflect.TypeOf(TenantLLM{})
	var tengo []string
	for i := range typ.NumField() {
		tengo = append(tengo, typ.Field(i).Name)
	}
	if !reflect.DeepEqual(tengo, quiero) {
		t.Fatalf("los campos de TenantLLM cambiaron.\ntengo:  %v\nquiero: %v\n\n"+
			"Si has añadido un campo para la credencial (o para cualquier forma de ella: enmascarada, "+
			"recortada, huella), PARA: la plataforma no la devuelve y este struct es lo que impide que "+
			"llegue a una plantilla. Si el campo nuevo es legítimo, añádelo a la lista de este test.", tengo, quiero)
	}

	// Y por si el nombre elegido despistara: ningún campo puede ser un `string` que suene a credencial.
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		for _, prohibido := range []string{"APIKey", "Key", "Secret", "Credential", "Token"} {
			if f.Name == prohibido {
				t.Errorf("TenantLLM.%s es un string: la credencial no se decodifica en esta capa", f.Name)
			}
		}
	}
}
