function integer(value = 0) {
  if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value);
  if (typeof value === 'string' && /^-?(0|[1-9][0-9]*)$/.test(value)) return value;
  throw new Error('invalid protobuf integer');
}

export function product(value) {
  if (!value || typeof value !== 'object') throw new Error('missing product');
  return {
    id: value.id ?? '',
    name: value.name ?? '',
    description: value.description ?? '',
    picture: value.picture ?? '',
    priceUsd: value.priceUsd == null ? null : {
      currencyCode: value.priceUsd.currencyCode ?? '',
      units: integer(value.priceUsd.units),
      nanos: integer(value.priceUsd.nanos),
    },
    categories: value.categories ?? [],
  };
}

export function products(values = []) {
  if (!Array.isArray(values)) throw new Error('invalid product list');
  return values.map(product);
}
