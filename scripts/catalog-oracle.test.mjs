import assert from 'node:assert/strict';
import { product, products } from './catalog-oracle.mjs';

const one = { id: 'one', name: 'first', priceUsd: { currencyCode: 'USD', units: 19, nanos: 0 } };
const two = { ...one, id: 'two', name: 'second' };
assert.deepEqual(product(one), product({ ...one, priceUsd: { currencyCode: 'USD', units: '19', nanos: '0' }, categories: [] }));
assert.notDeepEqual(product(one), product({ ...one, name: 'wrong' }));
assert.notDeepEqual(products([one, two]), products([two, one]));
assert.notDeepEqual(product({ id: 'one' }), product({ id: 'one', priceUsd: {} }));
assert.throws(() => product({ priceUsd: { units: 1.5 } }));
assert.throws(() => products({}));
